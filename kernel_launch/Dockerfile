FROM elyra/kernel-py:3.2.3

USER root

# Replace the script in the image with the new startup script.
COPY bootstrap-kernel.sh /usr/local/bin/
COPY launch_ipykernel.py /usr/local/bin/kernel-launchers/python/scripts/

USER $NB_UID
