# Restoring tmux sessions after a reboot

This sets up tmux-resurrect and tmux-continuum so tmux windows — layout,
working directories, window names — come back after a reboot or a crash.
claude-sandbox has no restore integration of its own yet; this is a stopgap.

## What it does and doesn't do

tmux-resurrect saves and restores tmux sessions: windows, split layouts,
window names, each pane's working directory, the active window/pane, and
grouped sessions. tmux-continuum autosaves on an interval and can restore
automatically when the tmux server starts.

Until claude-sandbox has its own restore integration, this gets every window
back at the right directory with the right name. A pane that was running a
sandbox comes back as a plain shell sitting in that directory — it does not
relaunch the sandbox for you. You do that by hand, e.g.:

```
claude-sandbox --new -- --resume
```

and pick the conversation from the picker.

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

Not enabled: `@continuum-boot 'on'`, which starts a detached tmux server at
boot via systemd. On Linux this starts only the server, with no terminal
attached. Combined with auto-restore, sessions would come back before login.
Left off pending operator decision 47.

## Using it

- `prefix + Ctrl-s` — save now. Do this manually before a planned reboot.
- `prefix + Ctrl-r` — restore the last save.

Saves live under `~/.local/share/tmux/resurrect/` (`@resurrect-dir`).
tmux-resurrect uses the XDG data dir only when `~/.tmux/resurrect` does not
exist; if that directory exists, it takes precedence and saves go there
instead. Continuum deletes
saves older than 30 days but always keeps at least 5, so you can restore an
earlier save if the latest one is bad.

What comes back is whatever existed at the last save:

- A window you close on purpose is gone at the next save — that's how you
  drop one from the saved layout.
- A window created in the last minute or so before a crash may be missing,
  since it wasn't captured by the last autosave.

## Safe shutdown before a reboot

Do this before a planned reboot, in order:

1. Stop autosaves: `tmux set -g @continuum-save-interval 0`
2. Save now with `prefix + Ctrl-s`.
3. Check that the `last` symlink in `~/.local/share/tmux/resurrect/` points
   at the new save: `ls -l ~/.local/share/tmux/resurrect/last`
4. Stop the server: `tmux kill-server`

Why: killing tmux during an autosave can leave `last` pointing at a partial
save, and `last` is what auto-restore and `prefix + Ctrl-r` use. Stopping
autosaves first and confirming `last` avoids that.

Killing tmux does NOT stop running sandboxes. Their containers keep running
detached; reattach with `claude-sandbox --attach`.

## Do not add claude-sandbox to `@resurrect-processes` yet

resurrect restores a program by typing its saved command line into the
pane's shell (`tmux send-keys … C-m`, from
`scripts/process_restore_helpers.sh`), so the shell underneath survives even
after the program exits.

For a sandbox session, the saved command line is wrong after a reboot:

- A plain `claude-sandbox` starts a *new* conversation instead of resuming
  the old one.
- `claude-sandbox --attach=<noun>` names a container that no longer exists
  after a reboot.
- Nothing prevents resuming a conversation that's already live elsewhere —
  two sessions attached to one conversation can corrupt its transcript.

So leave claude-sandbox out of `@resurrect-processes` for now. Sandbox panes
come back as a shell in the right directory, and you relaunch by hand.

## Planned integration (not built yet)

Tracked under work item `restore-short-window-names-and-per-group-1d0e`
(operator decisions 45b and 46a). resurrect will keep owning the layout;
claude-sandbox will add:

- A save hook (`@resurrect-hook-post-save-layout`, which receives the state
  file path) that records which conversation each sandbox pane is running.
- A `@resurrect-processes` entry whose substituted command, typed into the
  pane on restore, resumes that conversation — attaching to the container if
  it's still live, or leaving the shell alone if the conversation is already
  running somewhere else.

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
