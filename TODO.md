# TODO

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
