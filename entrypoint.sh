#!/bin/bash
set -e

TARGET_UID="${HOST_UID:-1000}"
TARGET_GID="${HOST_GID:-1000}"
DOCKER_SOCKET_GID="${DOCKER_GID:-}"

# Adjust claude user/group to match host so file ownership is correct
if [ "$(id -u claude)" != "$TARGET_UID" ] || [ "$(id -g claude)" != "$TARGET_GID" ]; then
    groupmod -o -g "$TARGET_GID" claude 2>/dev/null || true
    usermod -o -u "$TARGET_UID" -g "$TARGET_GID" claude 2>/dev/null || true
fi

# Grant docker socket access by adding claude to a group with the socket's GID
if [ -n "$DOCKER_SOCKET_GID" ]; then
    if ! getent group "$DOCKER_SOCKET_GID" >/dev/null 2>&1; then
        groupadd -g "$DOCKER_SOCKET_GID" hostdocker 2>/dev/null || true
    fi
    DOCKER_GROUP=$(getent group "$DOCKER_SOCKET_GID" | cut -d: -f1)
    usermod -aG "$DOCKER_GROUP" claude 2>/dev/null || true
fi

# Ensure home directory ownership
chown -R claude:claude /home/claude 2>/dev/null || true

# Symlink host .claude dir so hardcoded paths (e.g. plugin installPath
# values in installed_plugins.json) resolve inside the container.
if [ -n "${HOST_HOME:-}" ] && [ "$HOST_HOME" != "/home/claude" ] && [ ! -e "$HOST_HOME/.claude" ]; then
    mkdir -p "$HOST_HOME"
    ln -s /home/claude/.claude "$HOST_HOME/.claude"
fi

exec gosu claude "$@"
