# Base image: sandbox infrastructure WITHOUT the Claude Code CLI and WITHOUT
# the sandbox's own files.
#
# The CLI lives in its own image (Dockerfile.cli), and the sandbox binary,
# entrypoint, logstream, PROMPT_RALPH.md, Discord MCP bundle, managed-settings
# hooks and version stamp live in another (Dockerfile.tools). Both are copied
# onto this image — or onto a project's child image — by a generated one-layer
# "cap" at launch. Baking either here would put a frequently-changing layer in
# the middle of every child's ancestry; see spec/image-build.feature
# (CS-IMG-020, CS-IMG-048). This file therefore COPYs nothing from the build
# context: it rebuilds only when it changes itself.
#
# CACHE MOUNTS: every package-manager step keeps its downloads in a BuildKit
# cache mount with a FIXED id (claude-sandbox-apt, -apt-lists, -pip here;
# -npm, -go-mod, -go-build in Dockerfile.tools). Child Dockerfiles that use the
# same ids share the same cache, so a package downloads once per daemon, not
# once per image.

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
    python3-venv \
    tini

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

# The sandbox's own files arrive via the cap (Dockerfile.tools → COPY --link
# into /opt/claude-sandbox and /etc/claude-code/managed-settings.d); only the
# PATH entry and the ENTRYPOINT that names them live here. Nothing below runs
# at build time, so a child Dockerfile must not call claude-sandbox, ralph or
# the entrypoint in a RUN step either.
ENV PATH="/opt/claude-sandbox/bin:$PATH"

ENTRYPOINT ["/opt/claude-sandbox/bin/entrypoint.sh"]
CMD ["claude"]
