# Jupyter Kernel Controller

The controller allows users to create a custom resource "Kernel" (jupyter kernel).

It has been developed using Golang and [Kubebuilder](https://book.kubebuilder.io/quick-start.html).

## Spec

The user needs to specify the PodSpec for the Jupyter kernel. For example:

```yaml
apiVersion: jupyter.org/v1
kind: Kernel
metadata:
  name: foo
  namespace: default
spec:
  template:
    spec:
      containers:
        - env:
          - name: KERNEL_IDLE_TIMEOUT
            value: "1800"
          - name: KERNEL_ID
            value: 4cea8598-de71-43f7-bbff-0f60c6484595
          image: elyra/kernel-py:3.2.3
          name: main
          volumeMounts:
            - mountPath: /usr/local/bin/bootstrap-kernel.sh
              name: kernel-launch-vol
              subPath: bootstrap-kernel.sh
            - mountPath: /usr/local/bin/kernel-launchers/python/scripts/launch_ipykernel.py
              name: kernel-launch-vol
              subPath: launch_ipykernel.py
      restartPolicy: Never
      volumes:
        - configMap:
            defaultMode: 493
            name: kernel-launch-scripts
          name: kernel-launch-vol
```

The required fields are `containers[0].image` and (`containers[0].command` and/or `containers[0].args`). That is, the
user should specify what and how to run.

All other fields will be filled in with default value if not specified.

## Environment parameters

| Parameter           | Description                                           |
| ------------------- | ----------------------------------------------------- |
| KERNEL_SHELL_PORT   | The port of kernel shell socket, default:52317        |
| KERNEL_IOPUB_PORT   | The port of kernel iopub socket port, default:52318   |
| KERNEL_STDIN_PORT   | The port of kernel stdin socket port, default:52319   |
| KERNEL_HB_PORT      | The port of kernel hb socket port, default:52320      |
| KERNEL_CONTROL_PORT | The port of kernel control socket port, default:52321 |

## Commandline parameters

`metrics-addr`: The address the metric endpoint binds to. The default value is `:8080`.

`probe-addr`: The address the health endpoint binds to. The default value is `:8081`.

`enable-leader-election`: Enable leader election for controller manager. Enabling this will ensure there is only one
active controller manager. The default value is `false`.

## Implementation detail

This part is WIP as we are still developing.

Under the hood, the controller creates a pod to run the kernel instance, and a Service for it.

## Build, Run, Deploy

You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for
testing, or run against a remote cluster.

**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever
cluster `kubectl cluster-info` shows).

### Build and Run the Controller locally

In order to build the controller you will need to use Go 1.22 and up in order to have Go Modules support. You will also
need to have a k8s cluster.

1. Install the CRDs into the cluster and Run your controller

```shell
# build crd and deploy
kustomize build manifests/base > jupyter-kernel-controller.yaml
# deploy them
kubectl -n <your-namespaces> apply -f jupyter-kernel-controller.yaml
```

2. Verify that the controller is running in your namespace:

```
$ kubectl get pods -l app=kernel-controller -n <your-namespaces>
NAME                                           READY   STATUS    RESTARTS   AGE
kernel-controller-deployment-564d76877-mqsm8   1/1     Running   0          16s
```

## TODO

- Currently, only the startup script of the python kernel has been modified. When `KERNEL_LANGUAGE=python`, the socket
  port passed into the kernel can be customized.

## License

`Jupyter-Kernel-Controller` is distributed under the terms of the [Apache 2.0](https://spdx.org/licenses/Apache-2.0.html) license.
