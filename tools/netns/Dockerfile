ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}

RUN apt-get update && apt-get install -y --no-install-recommends nftables && rm -rf /var/lib/apt/lists/*
