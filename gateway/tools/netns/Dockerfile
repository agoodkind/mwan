ARG BASE_IMAGE=golang:1.27.1
FROM ${BASE_IMAGE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    nftables kea-dhcp4-server kea-dhcp6-server radvd \
    && rm -rf /var/lib/apt/lists/*
