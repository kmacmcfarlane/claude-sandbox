#!/bin/bash
set -e

# The entrypoint must run as root to remap UID/GID and chown files.
# Child Dockerfiles must end with USER root — see README.md.
if [ "$(id -u)" != "0" ]; then
    echo "ERROR: entrypoint.sh must run as root." >&2
    echo "Your Dockerfile.claude-sandbox likely ends with 'USER claude' instead of 'USER root'." >&2
    echo "Add 'USER root' as the last line. See: https://github.com/kmacmcfarlane/claude-sandbox#dockerfileclaude-sandbox" >&2
    exit 1
fi

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

    # Relocate build-time home (/home/claude) to the runtime home path
    # (e.g. /home/rt). Child Dockerfiles use /home/claude; the entrypoint
    # moves those files so they appear under the real $HOME at runtime.
    # SAFETY: skip anything already present at $TARGET_HOME — bind mounts
    # from the host (.claude, .ssh, .aws, .gitconfig) must never be
    # overwritten or have their permissions changed.
    if [ "$TARGET_HOME" != "/home/claude" ] && [ -d /home/claude ]; then
        mkdir -p "$TARGET_HOME"
        shopt -s dotglob nullglob
        for item in /home/claude/*; do
            base="$(basename "$item")"
            [ -e "$TARGET_HOME/$base" ] || mv "$item" "$TARGET_HOME/"
        done
        shopt -u dotglob nullglob
        rm -rf /home/claude
        ln -s "$TARGET_HOME" /home/claude
    fi
fi

# Own all non-bind-mounted files under the home dir so that files created
# as root during `docker build` match the host user's UID/GID at runtime.
PRUNE_ARGS=()
while IFS= read -r mp; do
    [[ "$mp" == "$TARGET_HOME"/* ]] && PRUNE_ARGS+=(-path "$mp" -prune -o)
done < <(awk '{print $5}' /proc/self/mountinfo)
find "$TARGET_HOME" "${PRUNE_ARGS[@]}" -print0 | xargs -0 chown "$TARGET_UID:$TARGET_GID" 2>/dev/null || true

# Grant docker socket access by adding user to a group with the socket's GID
if [ -n "$DOCKER_SOCKET_GID" ]; then
    if ! getent group "$DOCKER_SOCKET_GID" >/dev/null 2>&1; then
        groupadd -g "$DOCKER_SOCKET_GID" hostdocker 2>/dev/null || true
    fi
    DOCKER_GROUP=$(getent group "$DOCKER_SOCKET_GID" | cut -d: -f1)
    usermod -aG "$DOCKER_GROUP" "$TARGET_USER" 2>/dev/null || true
fi

exec gosu "$TARGET_USER" "$@"
