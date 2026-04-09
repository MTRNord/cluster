# Wraps the upstream continuwuity scratch-based image with a shell (debian:12-slim)
# so that Velero pre/post backup hooks can freeze the process via SIGSTOP/SIGCONT.
# {"$imagepolicy": "flux-system:forgejo-continuwuity-main"}
FROM forgejo.ellis.link/continuwuation/continuwuity:main AS upstream

# Build ldb and sst_dump from the exact RocksDB version continuwuity uses (10.10.1)
# so MANIFEST format is guaranteed compatible for recovery operations.
# {"$imagepolicy": "flux-system:docker-debian-12-slim"}
FROM debian:12-slim AS rocksdb-tools-builder
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates \
        libsnappy-dev liblz4-dev libzstd-dev zlib1g-dev libbz2-dev libgflags-dev \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch v10.10.1 https://github.com/facebook/rocksdb.git /rocksdb
WORKDIR /rocksdb
RUN cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DWITH_TOOLS=ON \
        -DWITH_TESTS=OFF \
        -DWITH_BENCHMARKS=OFF \
        -DWITH_SNAPPY=ON \
        -DWITH_LZ4=ON \
        -DWITH_ZSTD=ON \
        -DWITH_BZ2=ON \
        -DWITH_GFLAGS=ON \
        -DROCKSDB_BUILD_SHARED=OFF \
        -DUSE_RTTI=ON \
        -DCMAKE_CXX_FLAGS="-Wno-restrict" \
    && cmake --build build --target ldb sst_dump -j$(nproc)

# {"$imagepolicy": "flux-system:docker-debian-12-slim"}
FROM debian:12-slim
# Runtime deps for ldb/sst_dump
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        procps \
        libsnappy1v5 liblz4-1 libzstd1 zlib1g libbz2-1.0 libgflags2.2 \
    && rm -rf /var/lib/apt/lists/*

# Copy binary and shared libraries from the upstream scratch image
COPY --from=upstream /sbin/conduwuit /sbin/conduwuit
COPY --from=upstream /usr/lib/ /usr/lib/
COPY --from=upstream /etc/ssl/ /etc/ssl/

# Copy RocksDB tools built at matching version
COPY --from=rocksdb-tools-builder /rocksdb/build/tools/ldb /usr/local/bin/ldb
COPY --from=rocksdb-tools-builder /rocksdb/build/tools/sst_dump /usr/local/bin/sst_dump

ENV LD_LIBRARY_PATH=/usr/lib

ENTRYPOINT ["/sbin/conduwuit"]
