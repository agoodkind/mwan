# Builder for the publishing-enabled linux gateway binary (MWAN-358).
# The image carries the same pinned libyang and sysrepo the gateway runs,
# built from source on a Debian trixie base so the produced cgo binary
# links the gateway's glibc generation and libsysrepo ABI. Version pins
# arrive as build args from the caller; there are no defaults, so a bare
# build fails loudly rather than drifting from the gateway.
FROM golang:1.27-trixie

ARG LIBYANG_VERSION
ARG SYSREPO_VERSION
# The build flags have one home, the Makefile's WANCONFIG_*_CMAKE_FLAGS, and
# arrive here as build args so the dev-lane libraries carry the same compile
# options as the release's static archives (sysrepo's REPO_PATH and the
# compiled-out dlopen plugin machinery included).
ARG LIBYANG_CMAKE_FLAGS
ARG SYSREPO_CMAKE_FLAGS

RUN test -n "${LIBYANG_VERSION}" && test -n "${SYSREPO_VERSION}"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential cmake pkg-config git libpcre2-dev \
    && rm -rf /var/lib/apt/lists/*

RUN git clone --depth 1 --branch "${LIBYANG_VERSION}" \
        https://github.com/CESNET/libyang.git /usr/local/src/libyang \
    && cmake -S /usr/local/src/libyang -B /usr/local/src/libyang/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${LIBYANG_CMAKE_FLAGS} \
        -DBUILD_TESTING=OFF \
    && make -C /usr/local/src/libyang/build -j"$(nproc)" install \
    && ldconfig

RUN git clone --depth 1 --branch "${SYSREPO_VERSION}" \
        https://github.com/sysrepo/sysrepo.git /usr/local/src/sysrepo \
    && cmake -S /usr/local/src/sysrepo -B /usr/local/src/sysrepo/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${SYSREPO_CMAKE_FLAGS} \
        -DBUILD_TESTING=OFF \
    && make -C /usr/local/src/sysrepo/build -j"$(nproc)" install \
    && ldconfig
