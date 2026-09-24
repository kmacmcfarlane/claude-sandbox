#!/bin/bash -p
# -p (privileged mode): bash neither sources $BASH_ENV/$ENV nor imports
# functions, SHELLOPTS, BASHOPTS, CDPATH or GLOBIGNORE from the environment.
# This script runs as ROOT and its environment is the container's — every
# --env-file line reaches it — so without -p an env file naming a
# session-writable BASH_ENV would run as root before the first line below.
# Nothing else is executed before the environment is fixed up (CS-IMG-067).

# ROOT PATH. The image PATH puts /home/claude/.local/bin and the venv's bin —
# both writable by the session user (CS-IMG-052) — ahead of /usr/bin, and this
# script re-runs as root on every start of a kept container (docker start
# after a stop, a restart policy). A session-planted ~/.local/bin/awk would run
# as root. So the session's PATH is saved and the root part runs on the
# distribution's own directories only (/usr/local/* is left out: a child image
# may hand it to the user). It is restored for the session right before the
# hand-off. LD_PRELOAD, LD_LIBRARY_PATH and LD_AUDIT are dropped and NOT
# restored: the loader honours them for every root-run tool here and for gosu
# itself, whose exec happens before the privilege drop — and they had already
# loaded into this bash. A session that needs them sets them in its own shell.
_CS_SESSION_PATH="$PATH"
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
unset LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT
set -e

# The entrypoint must run as root to remap UID/GID and chown files.
# Child Dockerfiles must end with USER root — see README.md.
if [ "$(id -u)" != "0" ]; then
    echo "ERROR: entrypoint.sh must run as root." >&2
    echo "Your .claude-sandbox/Dockerfile likely ends with 'USER claude' instead of 'USER root'." >&2
    echo "Add 'USER root' as the last line. See: https://github.com/kmacmcfarlane/claude-sandbox#readme" >&2
    exit 1
fi

TARGET_UID="${HOST_UID:-1000}"
TARGET_GID="${HOST_GID:-1000}"
TARGET_USER="${HOST_USER:-claude}"
TARGET_HOME="${HOST_HOME:-/home/claude}"
DOCKER_SOCKET_GID="${DOCKER_GID:-}"

# Every mount point in this container, DECODED: /proc/self/mountinfo writes a
# space as \040, a tab as \011, a newline as \012 and a backslash as \134, so
# the raw field never equals the path of a mount whose name holds one. Both
# chowns below skip bind mounts by these paths. The raw field holds a backslash
# only as the start of such a three-digit escape, and %b reads \0nnn (up to
# three digits after \0) as octal, so each \ becomes \0 first: a bare \040
# followed by a digit ("a 1" is a\0401) would otherwise swallow that digit.
_CS_MOUNT_POINTS=()
while IFS= read -r _mp; do
    printf -v _mp '%b' "${_mp//\\/\\0}"
    _CS_MOUNT_POINTS+=("$_mp")
done < <(awk '{print $5}' /proc/self/mountinfo)

# _cs_prune_args DIR: sets _CS_PRUNE to "-path MP -prune -o" for every mount
# point below DIR. find's -path takes a glob, so \ * ? [ are escaped (backslash
# first) and a mount named "br[1]" or "st*r" is matched literally.
_cs_prune_args() {
    local mp
    _CS_PRUNE=()
    for mp in "${_CS_MOUNT_POINTS[@]}"; do
        [[ "$mp" == "$1"/* ]] || continue
        mp=${mp//\\/\\\\}; mp=${mp//\*/\\*}; mp=${mp//\?/\\?}; mp=${mp//\[/\\[}
        _CS_PRUNE+=(-path "$mp" -prune -o)
    done
}

# The container user: "claude" from the image on a first start, already
# renamed to the host user on a restart of a kept container (CS-IMG-068). Every
# step below is keyed on that name so a second run in the same container finds
# its work done and does it again only where something changed.
if getent passwd claude >/dev/null; then
    _CS_USER=claude
elif getent passwd "$TARGET_USER" >/dev/null; then
    _CS_USER="$TARGET_USER"
else
    echo "ERROR: entrypoint.sh: neither user 'claude' nor '$TARGET_USER' exists in this container." >&2
    exit 1
fi
_CS_GROUP="$(id -gn "$_CS_USER")"

# Adjust the user/group UID/GID to match the host
if [ "$(id -u "$_CS_USER")" != "$TARGET_UID" ] || [ "$(id -g "$_CS_USER")" != "$TARGET_GID" ]; then
    groupmod -o -g "$TARGET_GID" "$_CS_GROUP" 2>/dev/null || true
    usermod -o -u "$TARGET_UID" -g "$TARGET_GID" "$_CS_USER" 2>/dev/null || true
fi

# Rename user and home directory to match the host caller.
# This ensures paths recorded by Claude Code (e.g. plugin installPath
# values in installed_plugins.json) resolve identically inside and
# outside the container. Done on a restart (the user already carries the
# host name).
if [ "$_CS_USER" != "$TARGET_USER" ]; then
    usermod -l "$TARGET_USER" -d "$TARGET_HOME" "$_CS_USER" 2>/dev/null || true
    groupmod -n "$TARGET_USER" "$_CS_GROUP" 2>/dev/null || true
fi

# Relocate build-time home (/home/claude) to the runtime home path
# (e.g. /home/rt). Child Dockerfiles use /home/claude; the entrypoint
# moves those files so they appear under the real $HOME at runtime.
# SAFETY: skip anything already present at $TARGET_HOME — bind mounts
# from the host (.claude, .ssh, .aws, .gitconfig) must never be
# overwritten or have their permissions changed.
# Done on a restart: /home/claude is then the symlink this block leaves
# behind (-d follows it, -L tells), and the merge walk below would otherwise
# crawl the whole home merging it into itself.
if [ "$TARGET_USER" != "claude" ] && [ "$TARGET_HOME" != "/home/claude" ] \
    && [ -d /home/claude ] && [ ! -L /home/claude ]; then
    mkdir -p "$TARGET_HOME"

    # Bind-mount points under the target home — never recurse into or
    # overwrite these (host-provided .claude, .aws, .ssh, .gitconfig, a
    # direnv allow-state dir, etc.).
    declare -A _CS_MOUNTS=()
    for _mp in "${_CS_MOUNT_POINTS[@]}"; do
        case "$_mp" in "$TARGET_HOME"/*) _CS_MOUNTS["$_mp"]=1 ;; esac
    done

    # Merge /home/claude into $TARGET_HOME. Move any entry whose destination
    # is absent; when the destination dir exists only because Docker
    # pre-created it as a mount PARENT (e.g. ~/.local for a
    # ~/.local/share/direnv mount), recurse and merge its children rather
    # than skipping the whole subtree — otherwise build-time files such as
    # the claude binary under ~/.local/bin get left behind. Actual bind
    # mounts are never touched.
    _cs_merge() {
        local src="$1" dst="$2" child base
        if [ -n "${_CS_MOUNTS[$dst]:-}" ]; then return; fi
        if [ ! -e "$dst" ]; then mv "$src" "$dst"; return; fi
        if [ -d "$src" ] && [ -d "$dst" ]; then
            for child in "$src"/*; do
                [ -e "$child" ] || continue
                base="$(basename "$child")"
                _cs_merge "$child" "$dst/$base"
            done
        fi
    }

    shopt -s dotglob nullglob
    for item in /home/claude/*; do
        base="$(basename "$item")"
        _cs_merge "$item" "$TARGET_HOME/$base"
    done
    shopt -u dotglob nullglob
    rm -rf /home/claude
    ln -s "$TARGET_HOME" /home/claude
fi

# Own all non-bind-mounted files under the home dir so that files created
# as root during `docker build` match the host user's UID/GID at runtime.
# Only entries not already owned: a chown copies the file up into the
# container layer on overlay2 even when it changes nothing, and on a restart
# everything is already owned (CS-IMG-068).
_cs_prune_args "$TARGET_HOME"
find "$TARGET_HOME" "${_CS_PRUNE[@]}" \( ! -uid "$TARGET_UID" -o ! -gid "$TARGET_GID" \) -print0 \
    | xargs -0 --no-run-if-empty chown "$TARGET_UID:$TARGET_GID" 2>/dev/null || true

# Let the session user `pip install` into the base venv (CS-IMG-052). The venv
# is built as root (and children may pip-install into it as root at build
# time), so hand the session user its DIRECTORIES only: creating, renaming and
# unlinking entries needs write access to the containing directory, never to
# the file, so installs, upgrades and uninstalls all work while the files
# themselves stay root-owned. Chowning files would copy every one of them up
# into the container layer on overlay2 at each start — a child venv holding
# numpy or torch is hundreds of MB. The path is fixed (never $VIRTUAL_ENV, which
# an env file could point at /) and find never follows symlinks. Bind mounts
# never get their ownership changed, as with the home chown above: the block
# runs only when the venv is on the root filesystem and is not itself a mount
# point (a mount of the venv, /opt or /opt/claude-sandbox is the host's), and
# every mount point below it is pruned (_cs_prune_args; -xdev alone still lists the mount point
# directory itself). Installs live in the container layer and die with it.
VENV_DIR=/opt/claude-sandbox/venv
if [ -d "$VENV_DIR" ] && [ ! -L "$VENV_DIR" ] \
    && [ "$(stat -c %d "$VENV_DIR")" = "$(stat -c %d /)" ] \
    && ! mountpoint -q "$VENV_DIR"; then
    _cs_prune_args "$VENV_DIR"
    find "$VENV_DIR" -xdev "${_CS_PRUNE[@]}" -type d \
        \( ! -uid "$TARGET_UID" -o ! -gid "$TARGET_GID" \) \
        -exec chown "$TARGET_UID:$TARGET_GID" {} + 2>/dev/null || true
fi

# Grant docker socket access by adding user to a group with the socket's GID
if [ -n "$DOCKER_SOCKET_GID" ]; then
    if ! getent group "$DOCKER_SOCKET_GID" >/dev/null 2>&1; then
        groupadd -g "$DOCKER_SOCKET_GID" hostdocker 2>/dev/null || true
    fi
    DOCKER_GROUP=$(getent group "$DOCKER_SOCKET_GID" | cut -d: -f1)
    usermod -aG "$DOCKER_GROUP" "$TARGET_USER" 2>/dev/null || true
fi

# Hand off through the pid-class helper (spec/pidslot.feature, CS-PID-007): it
# advances this namespace's pid counter to the launcher-assigned class and
# execs tini, whose FORK lands the command on a pid no sibling sandbox shares —
# so Claude Code's ~/.claude/sessions/<pid>.json records stop colliding.
# (exec alone keeps this script's pid, which is why the fork matters.)
# The session gets its PATH back. Nothing after this line is looked up on
# PATH: gosu and the binary are named absolutely (CS-IMG-067).
PATH="$_CS_SESSION_PATH"
exec /usr/sbin/gosu "$TARGET_USER" /opt/claude-sandbox/bin/claude-sandbox pidslot -- "$@"
