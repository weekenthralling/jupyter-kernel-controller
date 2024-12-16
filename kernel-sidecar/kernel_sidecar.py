import logging
import socket
import base64
import json
import os
import sys
import time
import threading
from Cryptodome.Cipher import AES, PKCS1_v1_5
from Cryptodome.PublicKey import RSA
from Cryptodome.Util.Padding import unpad

from kubernetes import client, config
from kubernetes.client.rest import ApiException
from jupyter_client.blocking import BlockingKernelClient

logger = logging.getLogger(__name__)


def decrypt(payload_b64: bytes, private_key: str) -> str:
    """Decrypt payload and return the connection information."""
    # Decode the Base64 payload
    payload = str(base64.b64decode(payload_b64), "utf-8")
    payload = json.loads(payload)

    # Extract encrypted AES key and connection info
    encrypted_key = base64.b64decode(payload["key"])
    b64_connection_info = base64.b64decode(payload["conn_info"])

    # Decrypt AES key using RSA private key
    private_key_obj = RSA.importKey(base64.b64decode(private_key.encode()))
    cipher_rsa = PKCS1_v1_5.new(private_key_obj)
    aes_key = cipher_rsa.decrypt(encrypted_key, None)

    if aes_key is None:
        raise ValueError("Failed to decrypt the AES key. Check the private key.")

    # Decrypt the connection info using AES
    cipher_aes = AES.new(aes_key, AES.MODE_ECB)
    connection_info = unpad(cipher_aes.decrypt(b64_connection_info), 16)
    return connection_info.decode("utf-8")


def update_kernel_annotation(namespace: str, kernel_name: str, annotations_dict: dict):
    """Update Kubernetes CRD annotations with multiple key-value pairs."""
    # Load kubeconfig to configure Kubernetes client
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()

    api_instance = client.CustomObjectsApi()

    # Fetch the current CRD object
    crd = api_instance.get_namespaced_custom_object(
        group="jupyter.org",
        version="v1",
        namespace=namespace,
        plural="kernels",
        name=kernel_name,
    )

    # Retrieve existing annotations, if any
    annotations = crd.get("metadata", {}).get("annotations", {})

    # Update annotations with new key-value pairs
    annotations.update(annotations_dict)

    # Set the updated annotations back to the CRD
    crd["metadata"]["annotations"] = annotations

    # Replace the CRD with updated annotations
    api_instance.replace_namespaced_custom_object(
        group="jupyter.org",
        version="v1",
        namespace=namespace,
        plural="kernels",
        name=kernel_name,
        body=crd,
    )
    logger.debug(f"Successfully updated annotations for {kernel_name}.")


def check_idle(kernel_idle_timeout):
    """Check idle time and update the CRD annotation if idle time exceeds threshold."""
    global last_activity_timestamp
    while True:
        current_time = time.time()
        # Check if the idle timeout has been exceeded
        if (
            last_activity_timestamp is not None
            and current_time - last_activity_timestamp > kernel_idle_timeout
        ):
            # Set the "deletion" flag annotation on the CRD
            annotations_dict = {
                "jupyter.org/kernel-deletion": "true",  # Mark CRD as deletable
            }
            try:
                update_kernel_annotation(namespace, kernel_name, annotations_dict)
            except ApiException as e:
                logger.error(f"Failed to update annotations for {kernel_name}: {e}")
        time.sleep(60)


def monitor_activity(connection_info: dict):
    """Monitor kernel activity and update last_activity_timestamp when the kernel is busy."""
    global last_activity_timestamp
    client = BlockingKernelClient()
    client.load_connection_info(connection_info)
    client.start_channels()

    while True:
        try:
            msg = client.get_iopub_msg(timeout=1)
            if msg and msg["header"]["msg_type"] == "status":
                execution_state = msg["content"]["execution_state"]
                if execution_state == "busy":
                    last_activity_timestamp = time.time()
        except Exception:
            pass


def listen_kernel_creation(host="127.0.0.1", port=65432) -> str:
    """Listen for kernel creation requests and return connection information."""
    server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server_socket.bind((host, port))
    server_socket.listen(5)

    try:
        while True:
            client_socket, _ = server_socket.accept()
            data = ""
            with client_socket:
                while True:
                    buffer = client_socket.recv(1024).decode("utf-8")
                    if not buffer:
                        break  # End communication if no data is received
                    data += buffer
            # Decrypt and return the connection_info
            return decrypt(data.encode("utf-8"), private_key)
    except KeyboardInterrupt:
        logging.error("\nServer is shutting down...")
    finally:
        server_socket.close()
        logging.debug("Server has been closed.")


def start_activity_monitoring(connection_info: dict):
    """Start monitoring kernel activity and idle time in separate threads."""
    # Start idle check thread
    idle_check_thread = threading.Thread(target=check_idle, args=(kernel_idle_timeout,))
    idle_check_thread.daemon = True
    idle_check_thread.start()
    # Start activity monitoring thread
    activity_monitor_thread = threading.Thread(
        target=monitor_activity, args=(connection_info,)
    )
    activity_monitor_thread.daemon = True
    activity_monitor_thread.start()


if __name__ == "__main__":
    # Load environment variables
    private_key = os.getenv("PRIVATE_KEY")
    namespace = os.getenv("NAMESPACE")
    kernel_name = os.getenv("NAME")
    pod_ip = os.getenv("POD_IP")
    kernel_idle_timeout = os.getenv("KERNEL_IDLE_TIMEOUT")

    if private_key is None:
        raise ValueError("Private key is not set in environment variables.")

    if namespace is None:
        raise ValueError("NAMESPACE is not set in environment variables.")

    if kernel_name is None:
        raise ValueError("CRD NAME is not set in environment variables.")

    if pod_ip is None:
        host_name = socket.gethostname()
        pod_ip = socket.gethostbyname(host_name)

    # Listen for kernel creation and obtain connection info
    connection_info = listen_kernel_creation()

    if connection_info is None:
        logger.error("Failed to get connection_info.")
        sys.exit(1)

    # Add pod IP address to connection_info
    connection_info = json.loads(connection_info)
    logger.debug(f"Received connection_info: {connection_info}")
    connection_info = connection_info | {"ip": pod_ip}
    # Update kernel annotations with connection info
    try:
        # Convert connection_info to string and set as annotation value
        connection_info_str = json.dumps(connection_info)
        update_kernel_annotation(
            namespace,
            kernel_name,
            {"jupyter.org/kernel-connection-info": connection_info_str},
        )
    except ApiException as e:
        logger.error(f"Failed to update annotations for {kernel_name}: {e}")
        sys.exit(1)

    if kernel_idle_timeout is not None:
        last_activity_timestamp = time.time()
        start_activity_monitoring(connection_info)
