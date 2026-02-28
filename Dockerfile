FROM debian:bookworm-slim

# Install base utilities
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    gnupg \
    gosu \
    make \
    jq \
    less \
    openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Install Docker CLI + compose plugin (no daemon)
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg \
       | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
       https://download.docker.com/linux/debian bookworm stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
       docker-ce-cli \
       docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js LTS (for Claude Code CLI and output filters)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create non-root user (UID/GID adjusted at runtime by entrypoint)
RUN useradd -m -s /bin/bash claude

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

COPY bin/ /opt/claude-sandbox/bin/
COPY logstream/ /opt/claude-sandbox/logstream/
RUN chmod +x /opt/claude-sandbox/bin/*

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["claude"]
