# syntax=docker/dockerfile:1

# Compose uses these stages to build and test the Linux gateway and package
# releases. Linux builds use the OVS parser library and headers from a
# separate stage alongside libyang and sysrepo.

FROM golang:1.27.4-trixie AS base
ARG LIBYANG_VERSION
ARG SYSREPO_VERSION
ARG LIBYANG_COMMIT
ARG SYSREPO_COMMIT
ARG LIBYANG_CMAKE_FLAGS="-DCMAKE_DISABLE_FIND_PACKAGE_XXHash=ON"
ARG SYSREPO_CMAKE_FLAGS="-DREPO_PATH=/etc/sysrepo -DSR_HAVE_DLOPEN=OFF -DSYSREPO_UMASK=007 -DSYSREPO_GROUP=sysrepo -DNACM_SRMON_DATA_PERM=660 -DENABLE_EXAMPLES=OFF -DENABLE_SYSREPOCTL=OFF -DENABLE_SYSREPOCFG=OFF -DENABLE_SYSREPO_PLUGIND=OFF"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential cmake pkg-config git libpcre2-dev nftables util-linux iputils-ping \
        curl jq clang llvm libbpf-dev \
    && rm -rf /var/lib/apt/lists/*

RUN test -n "${LIBYANG_VERSION}" && test -n "${LIBYANG_COMMIT}" \
    && git clone --depth 1 --branch "${LIBYANG_VERSION}" https://github.com/CESNET/libyang.git /usr/local/src/libyang \
    && test "$(git -C /usr/local/src/libyang rev-parse HEAD)" = "${LIBYANG_COMMIT}" \
    && cmake -S /usr/local/src/libyang -B /usr/local/src/libyang/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${LIBYANG_CMAKE_FLAGS} -DENABLE_TOOLS=ON -DBUILD_TESTING=OFF \
    && cmake --build /usr/local/src/libyang/build --parallel \
    && cmake --install /usr/local/src/libyang/build \
    && ldconfig

RUN test -n "${SYSREPO_VERSION}" && test -n "${SYSREPO_COMMIT}" \
    && git clone --depth 1 --branch "${SYSREPO_VERSION}" https://github.com/sysrepo/sysrepo.git /usr/local/src/sysrepo \
    && test "$(git -C /usr/local/src/sysrepo rev-parse HEAD)" = "${SYSREPO_COMMIT}" \
    && cmake -S /usr/local/src/sysrepo -B /usr/local/src/sysrepo/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${SYSREPO_CMAKE_FLAGS} -DBUILD_TESTING=OFF \
    && cmake --build /usr/local/src/sysrepo/build --parallel \
    && cmake --install /usr/local/src/sysrepo/build \
    && ldconfig

FROM base AS static-libraries
ARG LIBYANG_CMAKE_FLAGS
ARG SYSREPO_CMAKE_FLAGS
RUN cmake -S /usr/local/src/libyang -B /tmp/libyang-static \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/mwan/cgo \
        ${LIBYANG_CMAKE_FLAGS} -DENABLE_TOOLS=OFF -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTING=OFF \
    && cmake --build /tmp/libyang-static --parallel \
    && cmake --install /tmp/libyang-static \
    && cmake -S /usr/local/src/sysrepo -B /tmp/sysrepo-static \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/mwan/cgo \
        -DCMAKE_PREFIX_PATH=/opt/mwan/cgo ${SYSREPO_CMAKE_FLAGS} \
        -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTING=OFF \
    && cmake --build /tmp/sysrepo-static --parallel \
    && DESTDIR=/tmp/sysrepo-install cmake --install /tmp/sysrepo-static \
    && cp -R /tmp/sysrepo-install/opt/mwan/cgo/. /opt/mwan/cgo/
COPY gateway/pkgconfig.awk /tmp/pkgconfig.awk
RUN awk -f /tmp/pkgconfig.awk /opt/mwan/cgo/lib/pkgconfig/libyang.pc > /tmp/libyang.pc \
    && mv /tmp/libyang.pc /opt/mwan/cgo/lib/pkgconfig/libyang.pc

FROM golang:1.27.4-trixie AS ovs-parser
ADD --checksum=sha256:43a1398a2099e7c19e90566b2ddcc4aed087943f8c797578edd8215f33c3c072 https://linux.mellanox.com/public/repo/doca/3.5.0/ubuntu22.04/x86_64/pool/openvswitch-source_3.5.0052-1_all.deb /tmp/ovs.deb
WORKDIR /usr/src/openvswitch
COPY gateway/ovs.mk /tmp/ovs.mk
RUN dpkg-deb -x /tmp/ovs.deb /tmp/ovs-package \
    && tar -xzf /tmp/ovs-package/usr/src/openvswitch/openvswitch.tar.gz --strip-components=1 \
    && ./configure --disable-introspect --disable-ssl --disable-shared --enable-static \
    && make -j4 -f Makefile -f /tmp/ovs.mk mwan-parser \
    && install -D -m 0644 lib/.libs/libopenvswitch.a /opt/parser/lib/libopenvswitch.a \
    && strip --strip-debug /opt/parser/lib/libopenvswitch.a \
    && mkdir -p /opt/parser/include \
    && find config.h include lib -name '*.h' -exec cp --parents {} /opt/parser/include/ \;

FROM base AS builder
COPY --from=ovs-parser /opt/parser /opt/parser
ENV GOWORK=off
WORKDIR /src/gateway
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/bufbuild/buf/cmd/buf@v1.66.1 \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1

FROM builder AS release
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/usr/local/bin go install goodkind.io/go-makefile/cmd/go-mk@main
RUN apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*
COPY --from=static-libraries /opt/mwan/cgo /opt/mwan/cgo
ENV PKG_CONFIG_PATH=/opt/mwan/cgo/lib/pkgconfig

FROM builder AS network-tests
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        iproute2 kea-dhcp4-server kea-dhcp6-server radvd \
    && rm -rf /var/lib/apt/lists/*

FROM network-tests AS systemd-tests
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        systemd systemd-sysv systemd-resolved dbus iproute2 udev \
    && install -m 0644 /dev/null /etc/mwan-systemd-test-image \
    && rm -rf /var/lib/apt/lists/*
ENV container=docker
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]

FROM builder AS drift
RUN --mount=type=bind,source=gateway,target=/src/gateway \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -o /usr/local/bin/mwan ./cmd/mwan
COPY gateway/go.mod /src/gateway/go.mod
COPY provider/go.mod /src/provider/go.mod
WORKDIR /tmp
RUN go work init /src/gateway /src/provider
ENV GOWORK=/tmp/go.work
WORKDIR /src/provider
