#!/bin/sh
set -eu

PUID="${PUID:-}"
PGID="${PGID:-}"

if [ -n "$PUID" ] || [ -n "$PGID" ]; then
    if [ -z "$PUID" ] || [ -z "$PGID" ]; then
        echo "PUID 和 PGID 必须同时设置" >&2
        exit 1
    fi
    case "$PUID" in
        *[!0-9]*) echo "PUID 必须是非负整数，当前值: $PUID" >&2; exit 1 ;;
    esac
    case "$PGID" in
        *[!0-9]*) echo "PGID 必须是非负整数，当前值: $PGID" >&2; exit 1 ;;
    esac
fi

if [ "$(id -u)" -eq 0 ]; then
    if [ -n "$PUID" ]; then
        exec su-exec "$PUID:$PGID" magnet-to-strm "$@"
    fi
fi

exec magnet-to-strm "$@"
