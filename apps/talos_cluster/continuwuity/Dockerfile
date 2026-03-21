# Wraps the upstream continuwuity scratch-based image with a shell (debian:12-slim)
# so that Velero pre/post backup hooks can freeze the process via SIGSTOP/SIGCONT.
FROM forgejo.ellis.link/continuwuation/continuwuity:main AS upstream

FROM debian:12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends procps \
    && rm -rf /var/lib/apt/lists/*

# Copy binary and shared libraries from the upstream scratch image
COPY --from=upstream /sbin/conduwuit /sbin/conduwuit
COPY --from=upstream /usr/lib/ /usr/lib/
COPY --from=upstream /etc/ssl/ /etc/ssl/

ENV LD_LIBRARY_PATH=/usr/lib

ENTRYPOINT ["/sbin/conduwuit"]
