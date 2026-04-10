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

# Install Claude Code CLI
USER claude
RUN curl -fsSL https://claude.ai/install.sh | bash
USER root
ENV PATH="/home/claude/.local/bin:$PATH"

COPY entrypoint.sh /opt/claude-sandbox/bin/entrypoint.sh
RUN chmod +x /opt/claude-sandbox/bin/entrypoint.sh

COPY bin/ /opt/claude-sandbox/bin/
COPY logstream/ /opt/claude-sandbox/logstream/
COPY PROMPT_RALPH.md /opt/claude-sandbox/PROMPT_RALPH.md
RUN chmod +x /opt/claude-sandbox/bin/*
ENV PATH="/opt/claude-sandbox/bin:$PATH"

ENTRYPOINT ["/opt/claude-sandbox/bin/entrypoint.sh"]
CMD ["claude"]
