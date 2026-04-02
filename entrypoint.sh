#!/bin/bash
set -e

TARGET_UID="${HOST_UID:-1000}"
TARGET_GID="${HOST_GID:-1000}"
TARGET_USER="${HOST_USER:-claude}"
TARGET_HOME="${HOST_HOME:-/home/claude}"
DOCKER_SOCKET_GID="${DOCKER_GID:-}"

# Adjust claude user/group UID/GID to match host
if [ "$(id -u claude)" != "$TARGET_UID" ] || [ "$(id -g claude)" != "$TARGET_GID" ]; then
    groupmod -o -g "$TARGET_GID" claude 2>/dev/null || true
    usermod -o -u "$TARGET_UID" -g "$TARGET_GID" claude 2>/dev/null || true
fi

# Rename user and home directory to match the host caller.
# This ensures paths recorded by Claude Code (e.g. plugin installPath
# values in installed_plugins.json) resolve identically inside and
# outside the container.
if [ "$TARGET_USER" != "claude" ]; then
    usermod -l "$TARGET_USER" -d "$TARGET_HOME" claude 2>/dev/null || true
    groupmod -n "$TARGET_USER" claude 2>/dev/null || true
    mkdir -p "$TARGET_HOME"
    chown "$TARGET_UID:$TARGET_GID" "$TARGET_HOME"
else
    chown -R claude:claude /home/claude 2>/dev/null || true
fi

# Grant docker socket access by adding user to a group with the socket's GID
if [ -n "$DOCKER_SOCKET_GID" ]; then
    if ! getent group "$DOCKER_SOCKET_GID" >/dev/null 2>&1; then
        groupadd -g "$DOCKER_SOCKET_GID" hostdocker 2>/dev/null || true
    fi
    DOCKER_GROUP=$(getent group "$DOCKER_SOCKET_GID" | cut -d: -f1)
    usermod -aG "$DOCKER_GROUP" "$TARGET_USER" 2>/dev/null || true
fi

exec gosu "$TARGET_USER" "$@"
