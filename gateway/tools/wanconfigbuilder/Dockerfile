# Build the Linux gateway with libyang and libsysrepo (MWAN-358).
# Debian trixie matches the gateway's glibc and libsysrepo ABI.
# The caller must supply pinned dependency versions as build arguments.
ARG RUST_VERSION
FROM rust:${RUST_VERSION}-slim-trixie AS native-rust-toolchain

FROM golang:1.27.3-trixie

COPY --from=native-rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=native-rust-toolchain /usr/local/rustup/ /usr/local/rustup/
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH=/usr/local/cargo/bin:${PATH}

RUN rustup component add clippy rustfmt

ARG LIBYANG_VERSION
ARG SYSREPO_VERSION
# The Makefile supplies WANCONFIG_*_CMAKE_FLAGS for both development and
# release archives, including sysrepo's repository path and plugin settings.
ARG LIBYANG_CMAKE_FLAGS
ARG SYSREPO_CMAKE_FLAGS

RUN test -n "${LIBYANG_VERSION}" && test -n "${SYSREPO_VERSION}"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential cmake pkg-config git libpcre2-dev nftables util-linux \
        meson ninja-build python3-pyelftools libnuma-dev clang libclang-dev \
    && rm -rf /var/lib/apt/lists/*

# The YANG gates require yanglint. Development builds enable its tools;
# release builds use the Makefile's static archive settings.
RUN git clone --depth 1 --branch "${LIBYANG_VERSION}" \
        https://github.com/CESNET/libyang.git /usr/local/src/libyang \
    && cmake -S /usr/local/src/libyang -B /usr/local/src/libyang/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${LIBYANG_CMAKE_FLAGS} \
        -DENABLE_TOOLS=ON \
        -DBUILD_TESTING=OFF \
    && make -C /usr/local/src/libyang/build -j"$(nproc)" install \
    && ldconfig

ARG RRDTOOL_VERSION
ARG RRDTOOL_SHA256

RUN test -n "${RRDTOOL_VERSION}" && test -n "${RRDTOOL_SHA256}"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        curl libcairo2-dev libpango1.0-dev libxml2-dev fontconfig \
        fonts-liberation poppler-utils \
    && rm -rf /var/lib/apt/lists/*

RUN curl --fail --location --retry 3 \
        "https://github.com/oetiker/rrdtool-1.x/releases/download/v${RRDTOOL_VERSION}/rrdtool-${RRDTOOL_VERSION}.tar.gz" \
        --output /tmp/rrdtool.tar.gz \
    && printf '%s  /tmp/rrdtool.tar.gz\n' "${RRDTOOL_SHA256}" | sha256sum --check - \
    && mkdir /usr/local/src/rrdtool \
    && tar -xzf /tmp/rrdtool.tar.gz --strip-components=1 -C /usr/local/src/rrdtool \
    && cd /usr/local/src/rrdtool \
    && ./configure --prefix=/usr/local --disable-perl --disable-python \
        --disable-ruby --disable-tcl --disable-lua --disable-docs \
    && make -j"$(nproc)" \
    && make install \
    && ldconfig \
    && rrdtool --version \
    && fc-match 'Liberation Mono'

RUN git clone --depth 1 --branch "${SYSREPO_VERSION}" \
        https://github.com/sysrepo/sysrepo.git /usr/local/src/sysrepo \
    && cmake -S /usr/local/src/sysrepo -B /usr/local/src/sysrepo/build \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
        ${SYSREPO_CMAKE_FLAGS} \
        -DBUILD_TESTING=OFF \
    && make -C /usr/local/src/sysrepo/build -j"$(nproc)" install \
    && ldconfig
