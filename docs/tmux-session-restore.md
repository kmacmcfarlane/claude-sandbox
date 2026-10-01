# Restoring tmux sessions after a reboot

This sets up tmux-resurrect and tmux-continuum so tmux windows — layout,
working directories, window names — come back after a reboot or a crash, and
wires in claude-sandbox's own hooks so sandbox panes come back too (see
[The claude-sandbox lines](#the-claude-sandbox-lines)).

## What it does and doesn't do

tmux-resurrect saves and restores tmux sessions: windows, split layouts,
window names, each pane's working directory, the active window/pane, and
grouped sessions. tmux-continuum autosaves on an interval and can restore
automatically when the tmux server starts.

This gets every window back at the right directory with the right name.
Without the claude-sandbox lines below, a pane that was running a sandbox
comes back as a plain shell sitting in that directory, and you relaunch it by
hand, e.g.:

```
claude-sandbox --new -- --resume
```

and pick the conversation from the picker. With them, each such pane runs
`claude-sandbox tmux restore`, which attaches to the container if it still
runs, or resumes the conversation in a new one.

## Install

The operator's `~/.tmux.conf` has no plugin manager (no tpm), so install by
cloning the two plugins directly. These clone lines are one-time shell
commands, run in a terminal. Never put them in `~/.tmux.conf` as `run-shell`
lines: tmux would re-run them on every config load and print "returned 128"
each time once the directories exist.

```
git clone https://github.com/tmux-plugins/tmux-resurrect ~/.tmux/plugins/tmux-resurrect
git clone https://github.com/tmux-plugins/tmux-continuum ~/.tmux/plugins/tmux-continuum
```

Append to `~/.tmux.conf`:

```
set -g @continuum-save-interval '1'
set -g @continuum-restore 'on'
set -g @resurrect-capture-pane-contents 'on'
run-shell ~/.tmux/plugins/tmux-resurrect/resurrect.tmux
run-shell ~/.tmux/plugins/tmux-continuum/continuum.tmux
```

The continuum `run-shell` line must be last. Continuum hooks the status line
to run its autosave, and a plugin that overwrites `status-right` after it
breaks autosave.

Reload the config:

```
tmux source-file ~/.tmux.conf
```

## What each option does

- `@continuum-save-interval '1'` — minutes between autosaves. Default is 15;
  `0` disables autosaving. Autosave needs the tmux status line turned on
  (the default).
- `@continuum-restore 'on'` — the last save is restored automatically when
  the tmux server starts. This only fires on server start, not when you
  re-source the config.
- `@resurrect-capture-pane-contents 'on'` — restores what was on screen in
  each pane, not just the running command. Caveat: this breaks if a pane's
  `default-command` contains `&&` or `||`. The operator's config sets no
  `default-command`, so this doesn't apply here.

`@continuum-boot 'on'` (operator answer 47 b) starts a detached tmux server
at boot from a systemd user unit (`tmux.service`), so with auto-restore on the
sessions are back before login. The operator's machine (hooper) sets it in
its own tmux config; add it to the block above when you want the same:

```
set -g @continuum-boot 'on'
```

On Linux it starts only the server, with no terminal attached, from the
user manager's environment (no login shell) — which is why the claude-sandbox
hooks below name the shim by its absolute path. The unit's `ExecStop` runs a
resurrect save before stopping the server, so a clean shutdown saves.

## Using it

- `prefix + Ctrl-s` — save now. Do this manually before a planned reboot.
- `prefix + Ctrl-r` — restore the last save.

Saves live under `${XDG_DATA_HOME:-~/.local/share}/tmux/resurrect/`
(`@resurrect-dir`; on the operator's machine `~/.local/share/tmux/resurrect/`).
tmux-resurrect uses the XDG data dir only when `~/.tmux/resurrect` does not
exist; if that directory exists, it takes precedence and saves go there
instead. An explicit `@resurrect-dir` overrides both. tmux-resurrect itself
deletes saves older than 30 days on every save (`remove_old_backups`) but
always keeps at least 5, so you can restore an earlier save if the latest one
is bad.

What comes back is whatever existed at the last save:

- A window you close on purpose is gone at the next save — that's how you
  drop one from the saved layout.
- A window created in the last minute or so before a crash may be missing,
  since it wasn't captured by the last autosave.

## Safe shutdown before a reboot

Do this before a planned reboot, in order:

1. Stop autosaves: `tmux set -g @continuum-save-interval 0`. Then wait a few
   seconds: an autosave already running in the background still finishes.
2. Save now with `prefix + Ctrl-s`, and wait for "Tmux environment saved!"
   to appear before going on.
3. Check that the `last` symlink in `~/.local/share/tmux/resurrect/` (or
   `~/.tmux/resurrect/last` if that directory exists) points at a complete
   save: `ls -l ~/.local/share/tmux/resurrect/last`. That is the new save,
   or an older one if nothing changed — resurrect drops a save identical to
   `last` and leaves `last` as it was.
4. Stop the server: `tmux kill-server`

Why: killing tmux during an autosave can leave `last` pointing at a partial
save, and `last` is what auto-restore and `prefix + Ctrl-r` use. Stopping
autosaves first and confirming `last` avoids that.

If you change your mind before step 4, turn autosave back on with
`tmux set -g @continuum-save-interval 1` (or `tmux source-file ~/.tmux.conf`).
After step 4 there is nothing to re-enable: a new tmux server reads
`~/.tmux.conf` and starts with autosave on.

Killing tmux does not stop running sandboxes; it leaves them running only
until the reboot. A reboot stops the containers and `--rm` removes them. So
before rebooting, let each session finish its turn and note each
conversation's session id (shown by `/status`) or its name, so you can resume
it afterwards with `claude-sandbox --new -- --resume <id-or-name>` (or pick it
from the `--resume` picker).

`claude-sandbox --attach` helps only when you kill tmux without rebooting:
the containers keep running detached, and you reattach by running it from the
project directory (attach filters by cwd). Joined sessions (`--join`) cannot
be reattached.

## The claude-sandbox lines

Add these four lines to `~/.tmux.conf`, before the two `run-shell` lines
(resurrect reads the options when it saves and restores). Replace
`/path/to/claude-sandbox` with your checkout:

```
set -g @resurrect-hook-post-save-layout '/path/to/claude-sandbox/bin/claude-sandbox tmux save'
set -g @resurrect-hook-pre-restore-all  '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --pin'
set -g @resurrect-hook-post-restore-all '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --rearm'
set -g @resurrect-processes '"claude-sandbox->claude-sandbox tmux restore --resurrected"'
```

If you already set `@resurrect-processes`, add the quoted entry to your list.

- The three hooks run in the tmux **server's** environment. With
  `@continuum-boot 'on'` that is the systemd user unit's, with no login
  shell, so they name the shim by its absolute path.
- The processes entry keeps the bare name: resurrect types it into each
  restored pane's own shell, which reads your shell's rc files. resurrect
  matches it against the first word of the saved command, which the shim
  makes `claude-sandbox`.
- `tmux save` records which conversation each sandbox pane holds, beside each
  save. `--pin` pins the save a restore reads and the panes that already
  exist. `--rearm` marks the panes the restore created and retypes the
  restore into pending ones that sat at a shell. All three never print,
  always exit 0, and finish within a few seconds; problems go to
  `~/.cache/claude-sandbox/tmux-save.log` and `tmux-restore.log`. The shim
  never builds for them: after a `git pull` they do nothing until your next
  ordinary `claude-sandbox` launch rebuilds the binary.

Check the wiring after reloading the config:

```
command -v claude-sandbox                     # in a pane: the processes entry needs it on your shell's PATH
tmux show -gv @resurrect-hook-post-save-layout
tmux show -gv @resurrect-hook-pre-restore-all
tmux show -gv @resurrect-hook-post-restore-all
tmux show -gv @resurrect-processes
```

If a hook is missing or fails, restores degrade rather than break: without
`--rearm`, the typed restores still read the pinned save and active sessions
come back, but a session that was waiting to be restored at a bare shell
drops out at the next save; without `--pin`, the typed restores read `last`
and `--rearm` does nothing. See the README's "tmux restore" sections for what
a restore decides in each pane.

### Optional: show the sparse-restore notice in the status line

When `--pin` finds that the save being restored has markedly fewer sandbox
panes than earlier tmux servers ended with, it sets the tmux option
`@claude-sandbox-notice` until the first `claude-sandbox` command you type
inside tmux prints and clears it. To see it the moment you attach, add this
line **before** continuum's `run-shell` line (continuum's own `status-right`
hook must stay last):

```
set -ga status-right '#{?@claude-sandbox-notice, #[reverse] #{@claude-sandbox-notice} #[default],}'
```

Note on hooks: upstream `docs/hooks.md` lists four hooks — post-save-layout,
post-save-all, pre-restore-all, pre-restore-pane-processes — and
`scripts/restore.sh` also runs `post-restore-all`.

## Uninstall

Remove the lines added above from `~/.tmux.conf`, then:

```
rm -rf ~/.tmux/plugins/tmux-resurrect ~/.tmux/plugins/tmux-continuum
```

Saved state under `~/.local/share/tmux/resurrect/` (or `~/.tmux/resurrect/`
if that directory exists) can be kept or removed independently.
