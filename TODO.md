# TODO

## Stray writes to the literal `~/.claude` when `CLAUDE_CONFIG_DIR` is set

Observed in a kappa-dev container whose config dir is relocated via
`CLAUDE_CONFIG_DIR`: the CLI still created `~/.claude/backups/` and
`~/.claude/downloads/` at session start. With the config dir relocated,
`~/.claude` is not a mount — it is container layer — so whatever lands in those
directories is silently lost at session exit. Either the CLI hardcodes those
paths past `CLAUDE_CONFIG_DIR` (upstream bug worth filing) or something else
writes there; identify which, then decide whether the launcher should mount or
redirect them.

## Prune old scratchpad session directories

With `CLAUDE_CODE_TMPDIR` rooted in the config-dir mount (CS-LNCH-034), scratch
state survives the container — which also means it accumulates: the CLI never
garbage-collects `<config>/tmp/claude-<uid>/<project>/<session-id>/` dirs
(verified against 2.1.245). Dozens of sessions leave dozens of dead dirs.
Consider launcher-side pruning of session dirs older than N days whose session
id no longer has a transcript, or at least document `rm -rf <config>/tmp` as
always-safe while no session is running.

## Offer microVM isolation via Docker Sandboxes (`sbx`)

Docker ships **Docker Sandboxes** (`sbx`) — agent sandboxes backed by microVMs on
a native hypervisor rather than containers, so the isolation boundary is a VM
kernel instead of a shared host kernel. That is a strictly stronger boundary than
what this project gets from `docker run`, and it is aimed squarely at exactly our
use case: running a coding agent unattended.

Status as of 2026-08-17 — **Linux is supported**, contrary to the
macOS/Windows-only claims that circulated at launch:

- **Linux:** Ubuntu 24.04+, x86-64 or arm64, KVM enabled, user in the `kvm`
  group; nested virtualization if the host is itself a VM. Installs via
  `curl -fsSL https://get.docker.com | sudo SBX=1 sh` (with Engine) or
  `REPO_ONLY=1` + `apt-get install docker-sbx` (sbx alone), or a binary from
  <https://github.com/docker/sbx-releases/releases>. `sbx` needs **neither**
  Docker Desktop nor Docker Engine. Note the packaging is `apt`-only and Fedora
  (hooper's OS) is not a listed platform, so the manual binary is the path here.
- **macOS:** Sonoma 14+, Apple silicon, `brew install docker/tap/sbx`.
- **Windows:** Windows 11, 64-bit Intel/AMD, Windows Hypervisor Platform enabled,
  `winget install -h Docker.sbx`.

Separately, **Docker VMM** — the same microVM engine folded into Docker Desktop —
is beta on macOS/Windows only in Desktop 4.86, with Linux support promised at GA.
That is the piece still genuinely pending.

Priority order for adding support: **Linux first** (the development platform),
then Windows. macOS has to wait — there is no Mac available to test on, and
shipping an untested platform path is worse than shipping none.

Open questions the design has to answer: whether `sbx` can honor the same-path
mount that `docker compose` volume resolution depends on; how host access
(Docker socket, AWS, git, SSH) survives a VM boundary that has no host socket to
bind; and whether the multi-session model (labels, attach, join) has any analogue.

## Align the base image's Python with the host

The image is `debian:bookworm-slim`, which pins Python to **3.11**. Development
happens on hooper (Fedora, **3.13**). Because `/home/claude` is the host's home,
both share `~/.cache/pre-commit`, and pre-commit's environments record the
interpreter that built them:

```
repo*/py_env-python3.11
      executable = /usr/bin/python3.11     # container's; absent on hooper
```

So whichever machine runs a hook second finds an environment bound to an
interpreter it does not have. pre-commit health-checks and rebuilds, so it
self-heals rather than failing — but every alternation pays a full reinstall,
and it hides real breakage in noise: a genuine environment failure looks exactly
like the routine rebuild. This surfaced in clustertool, where a push from hooper
died building a hook environment, but nothing about it is clustertool-specific —
any pre-commit repo touched from both sides has it.

Two independent fixes, and they solve different halves:

1. **Move the base to a release carrying Python 3.13** (Debian 13 "trixie" does).
   Aligns the versions so the shared cache is genuinely shareable. Note the
   builder stage is `golang:1.25-bookworm` and would move with it, so treat this
   as a base-image bump rather than a one-line edit, and rebuild every child
   image afterwards.
2. **Set `PRE_COMMIT_HOME` to a container-local path**, so the caches never
   collide in the first place. This works whatever the versions are, and keeps
   working when they drift again — which they will, since the host tracks Fedora
   and the image tracks Debian. Cost is that the container rebuilds hook
   environments from scratch rather than reusing the host's.

Doing 1 without 2 leaves the two caches merged and correct only for as long as
the versions happen to match.
