FROM elyra/kernel-py:3.2.3

USER root

# install chinese font
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
  fonts-noto-cjk

COPY requirements.txt .

RUN python -m pip install -r requirements.txt
RUN mplfonts init

COPY bootstrap-kernel.sh /usr/local/bin/
COPY launch_ipykernel.py /usr/local/bin/kernel-launchers/python/scripts/

USER $NB_UID
