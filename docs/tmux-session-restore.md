# Restoring tmux sessions after a reboot

This page sets up tmux-resurrect and tmux-continuum so tmux windows (layout, working
directories, window names) come back after a reboot, a crash or a tmux server restart, and
wires in claude-sandbox's hooks so the sandbox sessions in those windows come back too. It is
written for an operator setting this up for the first time; the README has the reference for
each command ([tmux pane marks](../README.md#tmux-pane-marks) through
[Restoring unattended](../README.md#restoring-unattended-the-resurrect-hooks)).

## What it does

tmux-resurrect saves and restores tmux sessions: windows, split layouts, window names, each
pane's working directory, the active window and pane, and grouped sessions. tmux-continuum
saves every minute and restores the last save when the tmux server starts; with
`@continuum-boot 'on'` a systemd user unit starts that server at boot.

On its own that brings each window back as a shell in the right directory. claude-sandbox
adds the sandbox sessions:

- **Pane mark.** Every launch, `--attach` and `--join` run inside tmux records in the pane
  option `@claude-sandbox` which sandbox the pane holds, and removes it when the session ends
  normally (an exit, a detach, a crash, an OOM kill). A session **stopped from outside** — a
  `docker stop`/`docker kill`, the host shutting down, or an end the launcher cannot read —
  keeps the mark as *pending*, so the session is restored rather than forgotten.
- **Window names.** A launch names its window after the conversation (`--name`, `/rename`) or
  the project folder; resurrect saves and restores the name.
- **Save hook.** After every resurrect save, `claude-sandbox tmux save` writes a sidecar file
  beside it recording each sandbox pane's mark and its conversation id.
- **Restore.** In each restored pane that ran a sandbox, resurrect types
  `claude-sandbox tmux restore --resurrected`. That attaches to the container if it still
  runs, else resumes the conversation in a new container (one pane at a time), else prints
  one line and leaves the shell.
- **Resurrect hooks.** `--pin` (before resurrect creates panes) pins the save being restored;
  `--rearm` (after) marks the panes this restore created and types the restore into those
  that were waiting at a bare shell.
- **Resume guard.** A launch that resumes a conversation by id (`-- --resume <id>`, which is
  what a restore runs) refuses with exit 4, naming the holder, when that conversation is already
  open in another sandbox or in host `claude`.

What it does not do:

- Nothing comes back that is not in the save. Closing a window is how you drop a session:
  the next save no longer has it.
- A session that **crashed or was OOM-killed** is not restored (its mark is removed, as for
  `/exit`). Only a stop from outside keeps it pending.
- A `--ralph` pane gets one line with its command; the loop is never restarted. A `--join`
  pane gets one line. `headless` and `--detach` sessions are never in a pane, so they are
  never marked.
- A window created in the minute before a crash or power loss may be missing: the last
  autosave did not see it.

## Setup

### Who owns `~/.tmux.conf`

On the operator's workstation (hooper) the tmux configuration belongs to the hooper repository
(its work item 857d; `~/.tmux.conf` is a symlink into it). That repository has no document of
the tmux setup yet, so the lines are given here; when hooper documents its config, this section
should link to it instead.

### Install the plugins

There is no plugin manager (no tpm), so clone the two plugins once, in a terminal. Never put
these lines in `~/.tmux.conf` as `run-shell` lines: tmux would re-run them on every config load
and print "returned 128" once the directories exist.

```
git clone https://github.com/tmux-plugins/tmux-resurrect ~/.tmux/plugins/tmux-resurrect
git clone https://github.com/tmux-plugins/tmux-continuum ~/.tmux/plugins/tmux-continuum
```

### The `~/.tmux.conf` lines

Replace `/path/to/claude-sandbox` with your checkout (the directory holding `bin/claude-sandbox`):

```
set -g @continuum-save-interval '1'
set -g @continuum-restore 'on'
set -g @continuum-boot 'on'
set -g @resurrect-capture-pane-contents 'on'

set -g @resurrect-hook-post-save-layout '/path/to/claude-sandbox/bin/claude-sandbox tmux save'
set -g @resurrect-hook-pre-restore-all  '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --pin'
set -g @resurrect-hook-post-restore-all '/path/to/claude-sandbox/bin/claude-sandbox tmux restore --rearm'
set -g @resurrect-processes '"claude-sandbox->claude-sandbox tmux restore --resurrected"'

run-shell ~/.tmux/plugins/tmux-resurrect/resurrect.tmux
run-shell ~/.tmux/plugins/tmux-continuum/continuum.tmux
```

Then reload: `tmux source-file ~/.tmux.conf`.

- **Order.** The options go before the two `run-shell` lines, and continuum's `run-shell` line
  is last: continuum hooks the status line to run its autosave, and anything that overwrites
  `status-right` after it breaks autosave.
- **The three hooks name the shim by its absolute path.** They run in the tmux **server's**
  environment. With `@continuum-boot 'on'` that is the systemd user manager's, with no login
  shell, so your shell's `PATH` does not apply.
- **The processes entry keeps the bare name.** resurrect types it into each restored pane's
  own shell, which reads your shell's rc files, so `claude-sandbox` must be on your
  interactive shell's `PATH`. resurrect matches the entry against the first word of each
  pane's saved command; the shim runs the launcher as `claude-sandbox` (not
  `bin/dist/claude-sandbox`) so that first word matches. If you already set
  `@resurrect-processes`, add the quoted entry to your list.

What each generic option does:

- `@continuum-save-interval '1'`: minutes between autosaves (default 15; `0` turns autosave
  off). Autosave needs the status line on (the default).
- `@continuum-restore 'on'`: restore the last save when the tmux server starts. It does not
  fire when you re-source the config.
- `@continuum-boot 'on'` (operator answer 47 b): continuum writes and enables a systemd user
  unit, `~/.config/systemd/user/tmux.service`, that starts a detached tmux server at boot, so
  the sessions are back before you log in. Its `ExecStop` runs a resurrect save before
  `tmux kill-server`, so a clean shutdown saves. continuum writes the unit only when it does
  not exist yet. A user unit starts at boot only when lingering is enabled for your user
  (`loginctl enable-linger`), otherwise at your first login; hooper 857d decides which.
- `@resurrect-capture-pane-contents 'on'`: restore what was on screen in each pane. It breaks
  if a pane's `default-command` contains `&&` or `||` (the operator's config sets none).

### Optional: the sparse-restore notice in the status line

When `--pin` finds that the save being restored has markedly fewer sandbox panes than earlier
tmux servers ended with (see [Choosing an earlier save](#choosing-an-earlier-save)), it sets the
tmux option `@claude-sandbox-notice` until the first `claude-sandbox` command you type inside
tmux prints and clears it. To see it the moment you attach after a boot, add this line
**before** continuum's `run-shell` line:

```
set -ga status-right '#{?@claude-sandbox-notice, #[reverse] #{@claude-sandbox-notice} #[default],}'
```

### Sessions started before the hooks

A session launched by a claude-sandbox from before the pane mark has no mark, so no save
records it. Detach from it (`ctrl-q ctrl-q`) and run `claude-sandbox --attach=<noun>` in the
same pane: the attach marks the pane and the next save records it.

## Verify the wiring

After reloading the config:

```bash
command -v claude-sandbox        # in a pane: the processes entry needs it on your shell's PATH
tmux show -gv @resurrect-hook-post-save-layout
tmux show -gv @resurrect-hook-pre-restore-all
tmux show -gv @resurrect-hook-post-restore-all
tmux show -gv @resurrect-processes
tmux run-shell 'test -x /path/to/claude-sandbox/bin/claude-sandbox && echo found'   # the hooks' path, as the server sees it
systemctl --user is-enabled tmux.service        # enabled, with @continuum-boot on
```

Then save and check what was recorded:

```bash
# prefix + C-s, wait for "Tmux environment saved!"
claude-sandbox tmux restore --list              # the newest run: N sandbox panes (a active, p pending)
claude-sandbox tmux restore --dry-run --all     # every sandbox pane of the save, each with its decision
```

Every pane running a sandbox should be listed, each with `attach` (its container runs). A
pane that is missing is unmarked (see the previous section) or the save hook did not run:
look in `~/.cache/claude-sandbox/tmux-save.log`. The first `claude-sandbox` command after a
`git pull` of the claude-sandbox checkout also rebuilds the binary, which the hooks need (see
[Degraded paths](#degraded-paths)).

## How a restore decides

For each pane, `claude-sandbox tmux restore` reads the pane's own pending mark, else the row
the save holds at the pane's coordinates (session, window, pane), and marks the pane pending
before anything else, so a restore cut short anywhere leaves it on the list. Then:

- **cleared** (one line, the mark removed): nothing to restore; a ralph run or a join (with
  the command to run by hand); the session already on screen in another pane; no conversation
  id recorded; a worktree whose name was never recorded; the conversation open elsewhere (with
  the attach command).
- **kept pending** (one line, `Retry: claude-sandbox tmux restore`, the exact manual command,
  and `--drop`): the project directory missing; docker not answering after 120 s; a paused or
  restarting container; a check that cannot tell; a container for the conversation being
  started by another launch.
- **attach**: the container still runs (a tmux server restart without a reboot). A config
  change since its launch is one note, never a prompt.
- **resume**: `claude-sandbox --new --worktree=<w>|--no-worktree [--model M] -- --resume <id>
  [--name N] <replayed flags>` in the project, with `CLAUDE_CONFIG_DIR` as the session had it.
  Only launch flags that do not widen access are replayed (operator answer 49); the others are
  named in one line, names only, so add them by hand if you want them. Everything else comes
  from the config cascade, as for any launch.

**Pacing.** Restores that start something run one at a time, on
`~/.cache/claude-sandbox/restore-start.lock`; a waiting pane prints which pane it waits for
(Ctrl-C skips it and leaves it pending). A resume holds the lock until its claude is up (at
most 60 s), plus 10 s unless `~/.claude.json` is linked or `CLAUDE_CONFIG_DIR` is set: in the
legacy layout several claudes starting at once can overwrite each other's writes to that one
file.
`claude-sandbox global-config migrate` (see the README's
[Global config](../README.md#global-config-claudejson)) removes the cause, and the 10 s with it.

## Drills

Run these once after setting up, and again after a change to the tmux config.

**Kill-server drill** (no reboot; safe, the sandboxes keep running):

1. With a few sandbox sessions running, save (`prefix + C-s`) and check `--list` as above.
2. `systemctl --user stop tmux.service` (or `tmux kill-server` without the unit). The
   containers keep running: they were created with a TTY, and losing the terminal does not
   stop them.
3. `systemctl --user start tmux.service` (or start `tmux`), and attach.
4. Each sandbox pane prints its restore and reattaches, one at a time. `claude-sandbox
   sessions` shows the same containers as before.

**Reboot drill** (with `@continuum-boot 'on'`):

1. Follow [Before a reboot](#before-a-reboot), then reboot.
2. After the boot, attach (`tmux attach`). The layout is back. Each sandbox pane shows its
   restore: it waits for docker if docker is still starting ("waiting for docker…"), then
   resumes its conversation in a new container, one pane at a time.
3. `claude-sandbox tmux restore --list`: the last run of the previous tmux server (the save its
   `ExecStop` took) holds as many sandbox panes as before the reboot, active or pending. The
   new server's run holds the same sessions once every pane is up.
4. `~/.cache/claude-sandbox/tmux-restore.log` has no new lines for `--pin` and `--rearm`, and
   no notice was set.

**Sparse-save drill** (shows the warning and the recovery; at least 2 sandbox sessions
running, throwaway ones if you prefer):

1. In `~/.tmux.conf`, comment out the `@resurrect-hook-pre-restore-all` and
   `@resurrect-processes` lines, then `systemctl --user stop tmux.service` and `start` it. The
   windows come back as plain shells (the containers still run, unattached).
2. Wait for an autosave (a minute) or press `prefix + C-s`: that save has no sandbox panes.
3. Put both lines back, stop and start the unit again. `--pin` finds the restored save sparse:
   the status element (if added) shows the notice.
4. `claude-sandbox tmux restore --list` prints the notice once (and clears it) and marks the
   newest saves `sparse (had M)`. Pick the stamp of the earlier good save.
5. `claude-sandbox tmux restore --dry-run --all --from <stamp>`, then
   `claude-sandbox tmux restore --all --from <stamp>`: each pane is armed and reattaches. The
   pane you are looking at is only marked (see [Typing safety](#typing-safety)): type
   `claude-sandbox tmux restore` in it.

## Procedures

### Restore one pane by hand

In the pane: `claude-sandbox tmux restore`. It reads the pane's pending mark, else the row
`last` holds at this pane's coordinates. `--from SAVE` reads another save: `last` (the
default), `previous` (the newest save of the previous tmux server), a stamp such as
`20260929T120000`, or a save's file name. Preview with `claude-sandbox tmux restore --dry-run`.

### Choosing an earlier save

resurrect writes a save every minute and keeps them for 30 days (and always at least 5);
claude-sandbox keeps one sidecar per save. `claude-sandbox tmux restore --list` prints the
saves of the last 7 days (`--all`: 30) newest first, grouped by tmux server, collapsing
consecutive saves that hold the same sessions into one line, and ends with the commands below
filled in with an example stamp and your save directory.

A save is **sparse** when it has at least 2 sandbox panes fewer, and at least a third fewer,
than the median of what the last saves of the previous 3 tmux servers held. After a bad restore
continuum writes a sparse save every minute, so `last` soon points at one; that is why the
comparison is with earlier servers, not the previous few saves. If you closed those sessions on
purpose, ignore it.

### Restore a whole layout from an earlier save

Use this when windows themselves are missing. Both ways repoint resurrect's `last` link at the
chosen save; `<dir>` is your save directory (see [Files](#files)).

**A, in the running tmux server (recommended):**

```
tmux set -g @continuum-save-interval 0      # so no autosave moves last meanwhile
ln -sf tmux_resurrect_<stamp>.txt <dir>/last
# prefix + C-r: resurrect creates the missing windows and panes; --pin pins <stamp>
tmux set -g @continuum-save-interval 1      # the value it had before (--list prints it)
```

resurrect creates only panes that do not exist yet. A pane that already exists at a saved
coordinate is left alone; arm its row with `--all --from <stamp>` (below) or type
`claude-sandbox tmux restore --from <stamp>` in it. With `renumber-windows on`, existing
windows may occupy saved indices: `claude-sandbox tmux restore --dry-run --all --from <stamp>`
first shows which rows land where.

**B, with a fresh tmux server:**

```
systemctl --user stop tmux.service          # its shutdown save moves last now
ln -sf tmux_resurrect_<stamp>.txt <dir>/last
systemctl --user start tmux.service         # continuum restores the repointed last
```

Without the unit: `tmux set -g @continuum-save-interval 0`, wait a few seconds,
`tmux kill-server`, the `ln -sf`, then start `tmux`. Stopping the server ends the launchers but
not the containers, so the restored panes reattach.

A reboot never restores a chosen save: its shutdown save moves `last` again.

### Arm an earlier save into the panes that exist: `--all --from`

When the windows are back but as shells (a restore that came up sparse),
`claude-sandbox tmux restore --all --from <stamp>` goes through every row of that save and, for
the pane at the row's coordinates, gives it the row as a pending mark and types
`claude-sandbox tmux restore --resurrected` into it, under the typing rules below. Each pane
then restores itself, one start at a time. Preview with `--dry-run --all --from <stamp>`. A row
is skipped when its pane is missing or not in the saved directory (the whole-layout commands are
printed after the list), when the pane runs claude-sandbox or holds another session's mark, is
the pane you typed the command in, or shows a session already on screen elsewhere, or when the
decision is final anyway (a ralph run, a join, the conversation open elsewhere). You type
`--all` yourself; no hook runs it. It works outside tmux too, on the default socket's server.

### Drop a pending pane: `--drop`

A pending mark is carried from save to save until a restore decides it. If you do not want the
session back, type `claude-sandbox tmux restore --drop` in the pane: it removes the pending
mark and restores nothing. Closing the window works too.

## Typing safety

claude-sandbox types into a pane in two cases: `--rearm` retyping the restore into a pane that
was pending at a bare shell when saved (resurrect typed nothing there), and `--all` arming a
pane. In both, the keys go in only when the pane is provably idle at its own shell's prompt:

- its current command is tmux's `default-shell`;
- the pane's own shell leads its terminal's foreground process group (so a running script, a
  program started from a wrapper, or a `su -` root shell does not count);
- it is not in copy mode and its window is not synchronized;
- no client is looking at it, through any session, read again right before the keys;
- its saved directory was compared with where it is.

The keys are `C-e C-u` (clear the line), then `claude-sandbox tmux restore --resurrected` and
Enter. Otherwise the pane is only marked pending, with the reason; type
`claude-sandbox tmux restore` in it yourself. resurrect itself types the processes entry into
every pane that ran a sandbox, without these checks, when the pane is created.

Residuals:

- In bash's vi insert mode `C-e` is inserted as a literal `^E` and `C-u` kills only back from
  the cursor, so text after the cursor joins the restore; in bash's vi command mode `C-e`
  switches that shell to emacs mode for the rest of its life; in zsh's vi insert mode `C-u`
  kills only back to where insert mode began.
- A shell function waiting in the builtin `read` counts as idle, so the restore text becomes
  its answer.
- The last check narrows, but cannot close, the moment between it and the keys; two `--all`
  runs at once have only that check between them.
- A typed `--resurrected` restore that starts and finishes between `--rearm`'s listing and its
  re-check is not seen: the row's pending mark is written back, and the next restore in that
  pane decides it again.
- At boot, keys resurrect types before a slow shell prompt can be lost.

If you use vi editing or leave `read` prompts open, preview with `--dry-run --all` and type the
restores yourself.

## Before a reboot

With `@continuum-boot 'on'`, in this order:

1. Let each session finish its turn. Do not `/exit` the sessions you want back: a clean exit
   removes the pane's mark, and the next save drops the session.
2. Optional: `tmux set -g @continuum-save-interval 0` and wait a few seconds, so no autosave
   runs alongside the shutdown save.
3. Save while the sandboxes still run: `prefix + C-s`, and wait for "Tmux environment saved!".
4. `claude-sandbox tmux restore --list`: the newest run holds every sandbox pane, active.
5. Reboot. Do not kill tmux first: the unit's `ExecStop` saves once more on the way down.

Without the unit, stop tmux yourself after step 4: `tmux kill-server`. The sandboxes keep
running until the reboot stops them.

**What changed (F1b).** At shutdown docker may stop the containers before the tmux server
saves. Before F1b, each launcher then ended normally and removed its pane's mark, so the save
the next boot restores had no sandbox rows, and the advice was to write down every
conversation id by hand. Now a session ended from outside — a `kill` or `stop` event, the host
shutting down (`systemctl is-system-running` says `stopping`), or an end the launcher cannot
read — leaves its pane pending with its last conversation, and the save hook records a pane
whose launcher still runs but whose container has stopped as pending too. The final save keeps
every sandbox row whichever stops first. Two of those signals are not yet confirmed on a real
shutdown (see [Host checks still owed](#host-checks-still-owed)), which is why step 3 saves
while everything still runs.

Killing tmux without rebooting stops no sandbox: the containers run on, and a restore (or
`claude-sandbox --attach` from the project directory) reattaches them. Joined sessions
(`--join`) cannot be reattached.

## Degraded paths

- **Docker not up yet at boot.** A restore waits up to 120 s for docker ("waiting for
  docker…", Ctrl-C to skip), then keeps the pane pending with the retry command. The save hook
  keeps every mark as it is while docker does not answer, so nothing drops out of the saves.
- **Stale binary after a `git pull`.** The hooks (`tmux save`, `--pin`, `--rearm`) never
  build: while the binary is older than the sources they do nothing and exit 0, until an
  ordinary `claude-sandbox` command rebuilds it. A `--resurrected` restore typed into a pane
  does build, one build at a time (the others wait for it, up to 10 minutes, then build
  anyway). So on a boot right after a pull, `--pin` and `--rearm` are skipped, and while the
  panes wait for the build continuum's autosaves get no sidecar and can move `last`: a pane whose
  restore starts after that reads a save with no record and does nothing. Run any
  `claude-sandbox` command (`claude-sandbox tmux restore --list` is a good one) after pulling
  and before a reboot or a tmux restart. If it happened, recover with `--list` and
  `--all --from <stamp>` of the last save before the boot.
- **Save hook missing or failing.** Saves get no sidecar (`--list` says `no record`), and a
  restore finds nothing recorded for a pane. Problems are in
  `~/.cache/claude-sandbox/tmux-save.log`.
- **`--pin` missing (or no pin).** The restores read `last`, which a continuum save in the
  middle of a slow restore can move, and `--rearm` does nothing: a session that was waiting at
  a bare shell is not re-marked and drops out at the next save. Active sessions still come back.
  A pin counts only for its own tmux server and for 10 minutes.
- **`--rearm` missing.** The restores still read the pin; only the pending sessions at bare
  shells drop out, as above.
- **Processes entry missing, or `claude-sandbox` not on the pane shell's `PATH`.** Panes come
  back as shells. With `--rearm` wired they are still marked pending: type
  `claude-sandbox tmux restore` in each, or run `claude-sandbox tmux restore --all` once.
- Problems of `--pin` and `--rearm` go to `~/.cache/claude-sandbox/tmux-restore.log`. Both never
  print, always exit 0 and stop starting work after 2 s and 5 s, so they never hold up
  resurrect.

## Window names

A launch that marks its pane names the window after the conversation (`--name` or a later
`/rename`) or else the project folder, with `rename-window`, which turns tmux's
`automatic-rename` off for that window, so resurrect saves and restores the name exactly. No
`tmux.conf` line is involved. A name you set yourself always wins. When the session ends the
window gets its automatic name back, unless the pane stays pending. A restore reclaims a window
it named before only when the saved row records that claude-sandbox named it. Every `#` is
removed from the name, since `rename-window` expands tmux formats, `#(command)` included. The
README's [tmux window names](../README.md#tmux-window-names) has the details.

## Files

- **Saves:** `${XDG_DATA_HOME:-~/.local/share}/tmux/resurrect/` — or `~/.tmux/resurrect/` if
  that directory exists, or `@resurrect-dir` when set. resurrect's own files are
  `tmux_resurrect_<stamp>.txt` and the `last` link. claude-sandbox adds, beside them:
  `tmux_resurrect_<stamp>.claude-sandbox.json` (one sidecar per save, 0600),
  `claude-sandbox-lifetimes.json` (one entry per tmux server), and
  `claude-sandbox-restore-pin.<server pid>.<time>.json` (pins, kept a day).
- **Cache:** `~/.cache/claude-sandbox/` holds `tmux-save.log`, `tmux-restore.log`,
  `restore-notice.json` and `restore-start.lock`.

## Host checks still owed

These behaviours the design relies on have not been observed on the host yet. Run them on a
quiet afternoon with throwaway sessions (and a scratch `CLAUDE_CONFIG_DIR` where noted), and
record the results in `spec/tmux.feature`, in the comment of the scenario each concerns
(CS-TMUX-061 for the record timing; CS-TMUX-071 for the shutdown, detach and exit-code checks;
CS-TMUX-067 for tpgid):

- [ ] **`claude --resume` record timing.** With a scratch `CLAUDE_CONFIG_DIR`, run
  `claude --resume <id>` and watch `<config dir>/sessions/`: does the `<pid>.json` record
  appear before the interactive screen, and when does it name `<id>`? (A restore's "up".)
- [ ] **docker events at daemon shutdown.** With a throwaway sandbox running,
  `docker events --filter type=container` in another terminal while you run `docker stop`, then
  `docker kill`, then `sudo systemctl stop docker`: does a daemon shutdown emit `kill` or `stop`
  for the containers it stops?
- [ ] **`systemctl is-system-running` during a shutdown.** A user unit whose `ExecStop` appends
  `systemctl is-system-running` to a file; reboot; does it read `stopping`?
- [ ] **docker client exit status on a detach,** for `docker start -ai` and `docker attach`
  (`ctrl-q ctrl-q`).
- [ ] **claude's exit codes** for `/exit`, Ctrl-D, a double Ctrl-C and SIGTERM.
- [ ] **tpgid at a prompt.** In a pane at a prompt, from another pane:
  `p=$(tmux display -p -t <pane> '#{pane_pid}'); sed 's/.*) //' /proc/$p/stat | awk '{print $6}'`
  prints `$p`; with `sleep 30` running in that pane it prints something else.
- [ ] **darwin:** `ps -o tpgid= -p <pane pid>` gives the same answers on macOS.
- [ ] **tmux 3.5a formats.** `tmux display -p '#{automatic-rename}'` prints `1` (or `on`) in a
  window tmux names itself; `tmux rename-window -t <scratch window> '#(echo expanded)'` names
  it `expanded` (why claude-sandbox removes `#`); and the status element above shows
  `@claude-sandbox-notice` next to continuum's hook (`tmux set -g @claude-sandbox-notice test`,
  then `tmux set -gu @claude-sandbox-notice`).
- [ ] **resurrect save and restore of a labelled window.** Launch a sandbox in a scratch
  window, save, run the kill-server drill: the window comes back with the same name, and after
  its restore `tmux show-options -w` shows `@claude-sandbox-label` again.

## The old stopgap scripts

`scripts/snapshot-sessions.py`, `scripts/restore-tmux.py` and the
`scripts/restore-snapshot-*.{md,sh}` files (untracked, in the operator's checkout) predate all
of this: a snapshot of running sessions written by hand before a reboot, and a script that
opened a window per session afterwards. They are superseded: the save hook records the same
conversation ids every minute, `--dry-run --all` prints the manual commands, `--from` reaches
30 days back, and the restore itself brings the panes back. They are to be deleted once a real
reboot has been restored this way.

## Uninstall

Remove the lines added above from `~/.tmux.conf`, disable the unit
(`systemctl --user disable tmux.service`), then:

```
rm -rf ~/.tmux/plugins/tmux-resurrect ~/.tmux/plugins/tmux-continuum
```

The saves and sidecars in the save directory can be kept or removed independently. Leftover
pane marks are harmless and go away with the tmux server.
