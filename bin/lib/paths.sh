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
        scripts)    echo ".claude-sandbox/scripts" ;;
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

# cs_collect_up <start_dir> <logical>
# Collect EVERY match of the logical path walking from the filesystem root down
# to <start_dir> (printed root-first, one per line). This is the cascade order:
# outermost defaults first, most-local file last (highest precedence).
cs_collect_up() {
    local dir="$1" logical="$2" new found=()
    new="$(_cs_foreign_map "$logical")" || { echo "cs_collect_up: unknown logical '$logical'" >&2; return 2; }
    while [ "$dir" != "/" ]; do
        [ -f "$dir/$new" ] && found+=("$dir/$new")
        dir="$(dirname "$dir")"
    done
    # found[] is leaf-first; print reversed (root-first)
    local i
    for ((i = ${#found[@]} - 1; i >= 0; i--)); do
        printf '%s\n' "${found[$i]}"
    done
}

# cs_merge_configs <out_file> <config_file>... (root-first order)
# Deep-merge configs with yq: scalars/maps from later (more local) files win,
# arrays append. `mounts` entries with the same host+container are overridden
# by the most local definition (e.g. flipping writable) instead of duplicated.
cs_merge_configs() {
    local out="$1"; shift
    yq eval-all '
        . as $item ireduce ({}; . *+ $item)
        | .mounts = ((.mounts // []) | reverse | unique_by((.host // "") + "|" + (.container // "")) | reverse)
    ' "$@" > "$out"
}

# cs_cascade_track_in_host <config_file>... (root-first order) -> "true"|"false"
# Most-local file that explicitly sets trackInHost wins; default false.
# grep-based so init works before yq is installed.
cs_cascade_track_in_host() {
    local f val="false" line
    for f in "$@"; do
        line="$(grep -E '^[[:space:]]*trackInHost:[[:space:]]*(true|false)([[:space:]]|$)' "$f" 2>/dev/null | tail -1)" || true
        if [ -n "$line" ]; then
            case "$line" in *true*) val="true" ;; *) val="false" ;; esac
        fi
    done
    echo "$val"
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
    # Sidecar's own .gitignore: keep runtime/secrets out of sidecar history.
    # Ensure each entry (append-only, no prompt — it's the sidecar's own file).
    touch "$sb/.gitignore"
    local _l
    for _l in 'temp/' 'env' 'ralph/'; do
        grep -qxF "$_l" "$sb/.gitignore" 2>/dev/null || printf '%s\n' "$_l" >> "$sb/.gitignore"
    done
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

# cs_prompt_track_in_host -> prints "true" | "false"
# Interactive prompt (default false). No tty -> false without prompting.
# Override for tests/automation with CS_TRACK_IN_HOST_ASSUME=true|false|y|n.
cs_prompt_track_in_host() {
    local ans="${CS_TRACK_IN_HOST_ASSUME:-}"
    if [ -z "$ans" ] && [ -e /dev/tty ]; then
        {
            echo "How should .claude-sandbox/ be version-controlled?"
            echo "  [y] Track in THIS repo   — own project; env/temp/ralph gitignored, rest committed"
            echo "  [N] Keep out of the repo — gitignore /.claude-sandbox/ + internal sidecar repo (default)"
        } >&2
        read -r -t 60 -p "Track in host repo? [y/N] " ans </dev/tty || ans=""
    fi
    case "$ans" in
        [Yy]|[Yy][Ee][Ss]|true|TRUE) echo "true" ;;
        *)                            echo "false" ;;
    esac
}

# cs_set_track_in_host <config_file> <true|false>
# Force the config's trackInHost to the given value (uncommented), replacing an
# existing commented or uncommented line, or appending if absent. Keeps the
# config self-consistent with the layout that gets set up.
cs_set_track_in_host() {
    local cfg="$1" val="$2"
    if grep -qE '^[[:space:]]*#?[[:space:]]*trackInHost:' "$cfg" 2>/dev/null; then
        sed -i -E "s/^[[:space:]]*#?[[:space:]]*trackInHost:.*/trackInHost: $val/" "$cfg"
    else
        printf '\ntrackInHost: %s\n' "$val" >> "$cfg"
    fi
}

# cs_seed_ralph_scaffold <project_dir> <scaffold_dir>
# Copies the scaffold's agent/ + scripts/ trees into .claude-sandbox/, never
# overwriting an existing file (project templates applied first win). Substitutes
# the __PROJECT_NAME__ placeholder and makes seeded python scripts executable.
# Prints a one-line summary.
cs_seed_ralph_scaffold() {
    local proj="$1" scaffold="$2" sb name name_esc created=0 skipped=0 src rel dest
    sb="$(cs_sandbox_dir "$proj")"
    name="$(basename "$proj")"
    # Escape sed replacement metachars (& and \) so a project dir name like
    # "foo&bar" substitutes literally. basename can't contain '/'.
    name_esc="$(printf '%s' "$name" | sed 's/[&\\]/\\&/g')"
    if [ ! -d "$scaffold" ]; then
        echo "cs_seed_ralph_scaffold: scaffold dir not found: $scaffold" >&2
        return 1
    fi
    while IFS= read -r src; do
        rel="${src#"$scaffold"/}"
        dest="$sb/$rel"
        if [ -e "$dest" ]; then skipped=$((skipped + 1)); continue; fi
        mkdir -p "$(dirname "$dest")"
        cp "$src" "$dest"
        # Only newly-created files get placeholder substitution.
        grep -q '__PROJECT_NAME__' "$dest" 2>/dev/null && \
            sed -i "s/__PROJECT_NAME__/$name_esc/g" "$dest"
        case "$dest" in *.py) chmod +x "$dest" ;; esac
        created=$((created + 1))
    done < <(find "$scaffold" -name '__pycache__' -prune -o -type f -print)
    echo "Ralph scaffolding: $created created, $skipped skipped (under $sb/agent, $sb/scripts)"
}
