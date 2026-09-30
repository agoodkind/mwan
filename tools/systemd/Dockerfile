ARG BASE_IMAGE
FROM ${BASE_IMAGE}

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        systemd systemd-sysv systemd-resolved dbus iproute2 udev \
    && rm -rf /var/lib/apt/lists/*

ENV container=docker
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
