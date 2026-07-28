# syntax=docker/dockerfile:1

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 magnet \
    && adduser -S -D -H -u 10001 -G magnet magnet \
    && mkdir -p /data \
    && chown magnet:magnet /data

USER magnet
WORKDIR /data

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["magnet-to-strm"]
CMD ["serve", "-config", "/etc/magnet-to-strm/config.toml"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/healthz || exit 1

# GitHub Actions builds both Linux binaries and selects the binary matching the
# image platform.
FROM runtime AS release

ARG TARGETARCH
COPY --chmod=0755 dist/magnet-to-strm-linux-${TARGETARCH} /usr/local/bin/magnet-to-strm
