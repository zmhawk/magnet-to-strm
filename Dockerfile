# syntax=docker/dockerfile:1

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates su-exec tzdata \
    && mkdir -p /data

WORKDIR /data

EXPOSE 8080
VOLUME ["/data"]

COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["serve", "-config", "/etc/magnet-to-strm/config.toml"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz || exit 1

# GitHub Actions builds both Linux binaries and selects the binary matching the
# image platform.
FROM runtime AS release

ARG TARGETARCH
COPY --chmod=0755 dist/magnet-to-strm-linux-${TARGETARCH} /usr/local/bin/magnet-to-strm
