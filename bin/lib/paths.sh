#!/usr/bin/env bash
#
# paths.sh — single resolver for claude-sandbox "foreign" file locations.
#
# claude-sandbox layers agent/workflow tooling onto host projects. All of its
# per-project files live under a single top-level .claude-sandbox/ directory.
#
# This is the ONE place foreign paths are mapped. Sourced by:
#   - bin/claude-sandbox  (host launcher)
#   - bin/ralph           (baked into the image, runs in-container)
#
# No hardcoded foreign paths should live anywhere else.

# Map a logical key to its relative path under the project root.
_cs_foreign_map() {
    case "$1" in
        config)     echo ".claude-sandbox/config.yaml" ;;
        dockerfile) echo ".claude-sandbox/Dockerfile" ;;
        env)        echo ".claude-sandbox/env" ;;
        ralph)      echo ".claude-sandbox/ralph" ;;
        agent)      echo ".claude-sandbox/agent" ;;
        *)          return 2 ;;
    esac
}

# cs_sandbox_dir <project_dir> -> "<project_dir>/.claude-sandbox"
cs_sandbox_dir() { printf '%s/.claude-sandbox\n' "$1"; }

# cs_resolve <project_dir> <logical>
# Prints the resolved absolute path under .claude-sandbox/.
cs_resolve() {
    local proj="$1" logical="$2" new
    new="$(_cs_foreign_map "$logical")" || { echo "cs_resolve: unknown logical '$logical'" >&2; return 2; }
    printf '%s\n' "$proj/$new"
}

# cs_find_up <start_dir> <logical>
# Walk parent directories (direnv-style) checking the .claude-sandbox/ path at
# each level. Prints the first hit, empty string if none found.
cs_find_up() {
    local dir="$1" logical="$2" new
    new="$(_cs_foreign_map "$logical")" || { echo "cs_find_up: unknown logical '$logical'" >&2; return 2; }
    while [ "$dir" != "/" ]; do
        if [ -f "$dir/$new" ]; then printf '%s\n' "$dir/$new"; return; fi
        dir="$(dirname "$dir")"
    done
}

# cs_layout_mode <project_dir> -> "new" | "none"
#   new    : .claude-sandbox/ exists (repo has adopted the layout)
#   none   : it does not (greenfield)
cs_layout_mode() {
    local proj="$1" sb
    sb="$(cs_sandbox_dir "$proj")"
    if [ -d "$sb" ]; then echo new; return; fi
    echo none
}

# Append missing lines to a .gitignore-style file, prompting first. Skipped when
# no tty is attached (e.g. cron-driven ralph) so it never blocks.
# Usage: _cs_gitignore_add <file> <line>...   Returns 0 if added, 1 if skipped.
# Override the prompt for tests/automation with CS_GITIGNORE_ASSUME=y|n.
_cs_gitignore_add() {
    local gi="$1"; shift
    local missing=() line
    for line in "$@"; do
        grep -qxF "$line" "$gi" 2>/dev/null || missing+=("$line")
    done
    [ ${#missing[@]} -eq 0 ] && return 0
    echo "These entries are missing from $gi:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    local ans="${CS_GITIGNORE_ASSUME:-}"
    if [ -z "$ans" ]; then
        if [ -e /dev/tty ]; then
            read -r -t 30 -p "Add them? [Y/n] " ans </dev/tty || ans=""
        else
            echo "(no tty; skipping .gitignore update)" >&2
            return 1
        fi
    fi
    [[ "$ans" =~ ^[Nn]$ ]] && { echo "Skipped .gitignore update." >&2; return 1; }
    if [ -s "$gi" ] && [ -n "$(tail -c1 "$gi" 2>/dev/null)" ]; then printf '\n' >> "$gi"; fi
    printf '%s\n' "${missing[@]}" >> "$gi"
    echo "Updated $gi" >&2
    return 0
}

# Ensure the .claude-sandbox/ skeleton, seed CLAUDE.md, host .gitignore, and
# (when not host-tracked) the sidecar git repo.
# Usage: cs_setup_layout <project_dir> <track_in_host: true|false>
cs_setup_layout() {
    local proj="$1" track="$2" sb host_is_git=false
    sb="$(cs_sandbox_dir "$proj")"
    mkdir -p "$sb/temp" "$sb/reports"
    git -C "$proj" rev-parse --is-inside-work-tree >/dev/null 2>&1 && host_is_git=true

    if [ ! -f "$sb/CLAUDE.md" ]; then
        cat > "$sb/CLAUDE.md" <<'SEED'
# .claude-sandbox/

claude-sandbox's per-project "foreign" files, consolidated out of the host tree.

- `config.yaml` — sandbox config
- `Dockerfile` — child image
- `env` — environment variables, secret, never committed
- `ralph/` — ralph loop runtime + logs
- `agent/` — workflow docs + backlog
- `temp/` — scratch (uncommittable)
- `reports/` — durable outputs (bench, parity diffs, QA logs)

## Committing changes here

When `trackInHost` is false (default), this directory is gitignored in the host
repo and keeps its OWN sidecar git repo for history. After grooming the backlog
or changing the agent flow, PROMPT the user to commit in the sidecar — do not
auto-commit:

    git -C .claude-sandbox add -A && git -C .claude-sandbox commit -m "..."
SEED
    fi

    if [ "$track" = "true" ]; then
        # Host-tracked: no sidecar. Ignore secrets (env), scratch (temp/), and the
        # ralph runtime dir (runlog/runlogs/lock/stop) — ephemeral state kept out
        # of the repo. The trailing
        # negations defensively re-include the tracked config/Dockerfile in case
        # the host repo has a bare `config.yaml`/`Dockerfile` ignore rule (common
        # in Go projects) that would otherwise swallow them. Negations are no-ops
        # when no such rule exists.
        [ "$host_is_git" = true ] && _cs_gitignore_add "$proj/.gitignore" \
            ".claude-sandbox/env" ".claude-sandbox/temp/" ".claude-sandbox/ralph/" \
            "!.claude-sandbox/config.yaml" "!.claude-sandbox/Dockerfile"
        return 0
    fi

    # Foreign-safe: whole dir ignored in host; sidecar repo holds history.
    [ "$host_is_git" = true ] && _cs_gitignore_add "$proj/.gitignore" "/.claude-sandbox/"
    [ -f "$sb/.gitignore" ] || printf 'temp/\nenv\n' > "$sb/.gitignore"
    if [ ! -e "$sb/.git" ]; then
        # Only init when the host won't also track it (a nested .git in a tracked
        # tree triggers git's embedded-repository warning).
        if [ "$host_is_git" = false ] || git -C "$proj" check-ignore -q "$sb"; then
            git -C "$sb" init -q && echo "Initialized sidecar git repo at $sb"
        else
            echo "Note: $sb is not gitignored by the host repo; skipping sidecar git init." >&2
            echo "  Add /.claude-sandbox/ to .gitignore to enable sidecar history." >&2
        fi
    fi
}
