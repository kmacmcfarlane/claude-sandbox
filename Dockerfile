# syntax=docker/dockerfile:1
# Base image: sandbox infrastructure WITHOUT the Claude Code CLI.
#
# The CLI lives in its own image (Dockerfile.cli) and is copied onto this
# image — or onto a project's child image — by a generated one-layer "cap"
# at launch. Installing it here would put a daily-changing layer in the
# middle of every child's ancestry; see spec/image-build.feature (CS-IMG-020).
#
# CACHE MOUNTS: every package-manager step keeps its downloads in a BuildKit
# cache mount with a FIXED id (claude-sandbox-apt, -apt-lists, -pip, -npm,
# -go-mod, -go-build). Child Dockerfiles that use the same ids share the same
# cache, so a package downloads once per daemon, not once per image.

# Build the claude-sandbox Go binary (launcher + in-container ralph runner).
FROM golang:1.25-bookworm AS builder
WORKDIR /src
# Dependencies first, in their own layer: this only re-downloads when go.mod or
# go.sum changes, not on every source edit — and the module cache mount means
# even that re-download is served from disk.
COPY go.mod go.sum ./
RUN --mount=type=cache,id=claude-sandbox-go-mod,target=/go/pkg/mod \
    go mod download
COPY assets.go ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY scaffold/ scaffold/
COPY scaffold-ralph/ scaffold-ralph/
COPY container-context.md notification-hooks.json mcp-servers.json PROMPT_RALPH.md ./
RUN --mount=type=cache,id=claude-sandbox-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=claude-sandbox-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/claude-sandbox ./cmd/claude-sandbox

FROM debian:bookworm-slim

# Let apt keep its downloads (the cache mount holds them); docker-clean would
# delete every .deb right after install.
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
    && echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

# Install base utilities
RUN --mount=type=cache,id=claude-sandbox-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=claude-sandbox-apt-lists,target=/var/lib/apt,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    gnupg \
    gosu \
    make \
    jq \
    less \
    openssh-client \
    python3 \
    python3-dev \
    python3-pip \
    python3-venv

# Python virtual environment for agent tooling (backlog CRUD, etc.)
ENV VIRTUAL_ENV=/opt/claude-sandbox/venv
RUN --mount=type=cache,id=claude-sandbox-pip,target=/root/.cache/pip \
    python3 -m venv $VIRTUAL_ENV \
    && $VIRTUAL_ENV/bin/pip install 'ruamel.yaml>=0.18,<1.0'
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

# Install Docker CLI + compose + buildx plugins (no daemon). buildx is what
# makes `docker build` inside the sandbox use BuildKit; without it the CLI
# silently falls back to the legacy builder and cache mounts / COPY --link
# are unavailable to builds run from a session.
RUN --mount=type=cache,id=claude-sandbox-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=claude-sandbox-apt-lists,target=/var/lib/apt,sharing=locked \
    install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg \
       | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
       https://download.docker.com/linux/debian bookworm stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       unzip \
       docker-ce-cli \
       docker-compose-plugin \
       docker-buildx-plugin

# Install Node.js LTS (for the output filters and MCP servers)
RUN --mount=type=cache,id=claude-sandbox-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=claude-sandbox-apt-lists,target=/var/lib/apt,sharing=locked \
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs

# AWS CLI v2
RUN curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip && \
    unzip -q /tmp/awscliv2.zip -d /tmp && \
    /tmp/aws/install && \
    rm -rf /tmp/aws /tmp/awscliv2.zip

# Create non-root user (UID/GID adjusted at runtime by entrypoint)
RUN useradd -m -s /bin/bash claude

# The Claude Code CLI arrives via the cap (Dockerfile.cli → COPY --link into
# /home/claude/.local); only its PATH entry lives here.
ENV PATH="/home/claude/.local/bin:$PATH"

# Baked artifacts. COPY --link makes each layer independent of what came
# before, so a change above does not force these to be re-copied.
COPY --link --chmod=755 entrypoint.sh /opt/claude-sandbox/bin/entrypoint.sh

# The Go binary serves as both the launcher and (via argv0) the ralph runner.
COPY --link --chmod=755 --from=builder /out/claude-sandbox /opt/claude-sandbox/bin/claude-sandbox
RUN ln -s /opt/claude-sandbox/bin/claude-sandbox /opt/claude-sandbox/bin/ralph
COPY --link logstream/ /opt/claude-sandbox/logstream/
COPY --link PROMPT_RALPH.md /opt/claude-sandbox/PROMPT_RALPH.md
ENV PATH="/opt/claude-sandbox/bin:$PATH"

# Discord notification MCP server (baked in so every project gets it for free)
COPY mcp/discord-notify/ /opt/claude-sandbox/mcp/discord-notify/
RUN --mount=type=cache,id=claude-sandbox-npm,target=/root/.npm \
    cd /opt/claude-sandbox/mcp/discord-notify \
    && npm install --production=false \
    && npx esbuild index.mjs --bundle --platform=node --format=esm --outfile=dist/index.mjs \
    && rm -rf node_modules

# Stamp the claude-sandbox version (git describe, passed by the launcher) so it
# is discoverable in-container ($CLAUDE_SANDBOX_VERSION / the version file) and on
# the host (image label). Placed last so a changed version only rebuilds this layer.
ARG CLAUDE_SANDBOX_VERSION=unknown
RUN echo "$CLAUDE_SANDBOX_VERSION" > /opt/claude-sandbox/version
ENV CLAUDE_SANDBOX_VERSION=$CLAUDE_SANDBOX_VERSION
LABEL org.opencontainers.image.revision=$CLAUDE_SANDBOX_VERSION

ENTRYPOINT ["/opt/claude-sandbox/bin/entrypoint.sh"]
CMD ["claude"]
