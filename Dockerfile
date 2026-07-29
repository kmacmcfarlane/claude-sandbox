# Build the claude-sandbox Go binary (launcher + in-container ralph runner).
FROM golang:1.25-bookworm AS builder
WORKDIR /src
# Dependencies first, in their own layer: this only re-downloads when go.mod or
# go.sum changes, not on every source edit.
COPY go.mod go.sum ./
RUN go mod download
COPY assets.go ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY scaffold/ scaffold/
COPY scaffold-ralph/ scaffold-ralph/
COPY container-context.md notification-hooks.json mcp-servers.json PROMPT_RALPH.md ./
RUN CGO_ENABLED=0 go build -o /out/claude-sandbox ./cmd/claude-sandbox

FROM debian:bookworm-slim

# Install base utilities
RUN apt-get update && apt-get install -y --no-install-recommends \
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
    && rm -rf /var/lib/apt/lists/*

# Python virtual environment for agent tooling (backlog CRUD, etc.)
ENV VIRTUAL_ENV=/opt/claude-sandbox/venv
RUN python3 -m venv $VIRTUAL_ENV \
    && $VIRTUAL_ENV/bin/pip install --no-cache-dir 'ruamel.yaml>=0.18,<1.0'
ENV PATH="$VIRTUAL_ENV/bin:$PATH"

# Install Docker CLI + compose plugin (no daemon)
RUN install -m 0755 -d /etc/apt/keyrings \
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
    && rm -rf /var/lib/apt/lists/*

# Install Node.js LTS (for Claude Code CLI and output filters)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# AWS CLI v2
RUN curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip && \
    unzip -q /tmp/awscliv2.zip -d /tmp && \
    /tmp/aws/install && \
    rm -rf /tmp/aws /tmp/awscliv2.zip

# Create non-root user (UID/GID adjusted at runtime by entrypoint)
RUN useradd -m -s /bin/bash claude

# Install Claude Code CLI and record version as a Docker label
USER claude
RUN curl -fsSL https://claude.ai/install.sh | bash
USER root
ENV PATH="/home/claude/.local/bin:$PATH"
RUN /home/claude/.local/bin/claude --version 2>/dev/null | head -1 > /opt/claude-sandbox/claude-version || echo "unknown" > /opt/claude-sandbox/claude-version

COPY entrypoint.sh /opt/claude-sandbox/bin/entrypoint.sh
RUN chmod +x /opt/claude-sandbox/bin/entrypoint.sh

# The Go binary serves as both the launcher and (via argv0) the ralph runner.
COPY --from=builder /out/claude-sandbox /opt/claude-sandbox/bin/claude-sandbox
RUN ln -s /opt/claude-sandbox/bin/claude-sandbox /opt/claude-sandbox/bin/ralph
COPY logstream/ /opt/claude-sandbox/logstream/
COPY PROMPT_RALPH.md /opt/claude-sandbox/PROMPT_RALPH.md
RUN chmod +x /opt/claude-sandbox/bin/*
ENV PATH="/opt/claude-sandbox/bin:$PATH"

# Discord notification MCP server (baked in so every project gets it for free)
COPY mcp/discord-notify/ /opt/claude-sandbox/mcp/discord-notify/
RUN cd /opt/claude-sandbox/mcp/discord-notify \
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
