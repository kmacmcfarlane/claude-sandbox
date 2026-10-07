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
  normally (an exit, a detach). A session **stopped from outside** — a
  `docker stop`/`docker kill`, the host shutting down, or an end the launcher cannot read —
  keeps the mark as *pending*, so the session is restored rather than forgotten. A session that
  **crashed or was OOM-killed** keeps it as *crashed*: a restore brings the pane back as a shell
  with one line naming the resume command, and never relaunches it.
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
- A session that **crashed or was OOM-killed** is not restarted. Its pane comes back as a
  shell at its folder with one hint line, for example:

  ```
  claude-sandbox: 'myproj' crashed in this pane on 2026-10-05 14:02 (exit 137, killed by the OOM killer); not restarted. Resume it with: cd /home/u/myproj && claude-sandbox --new --no-worktree -- --resume 0b6f…
  ```

  Run that command to resume it. A crash is a `claude` session whose container died with an
  exit code other than 0 or 78 (`tmuxpane.CleanExitCodes`) and no sign of a stop from outside;
  the conversation must be known (the save hook records it within a minute, or the launch's own
  `--resume <id>`). A crashed join or ralph run, or one whose conversation is not known yet, is
  forgotten as before. The hint is printed once, at the next restore (and the next hand launch
  in the pane prints the same command as a note); a crashed mark never expires — it stays
  until then, until `--drop`, or until the pane is gone.
- A `--ralph` pane gets one line with its command; the loop is never restarted. A `--join`
  pane gets one line. `headless` and `--detach` sessions are never in a pane, so they are
  never marked.
- A window created in the minute before a crash or power loss may be missing: the last
  autosave did not see it.

## Setup

### Who owns `~/.tmux.conf`

On the operator's workstation (hooper) the config is the hooper repository's `system/tmux.conf`,
which holds the claude-sandbox hook lines below, and `system/install-tmux.sh` installs it:
the installer copies the reviewed file to `~/.tmux.conf` (it is not a symlink), and an existing,
different file, or an old symlink from an earlier install, is backed up first.
The hooper repository's README has a section, "Workstation tmux (config + session restore)",
that links back to this document for the restore internals. Nothing here needs adding by hand
on hooper, and the installer also clones the two plugins.

On any other host, the operator adds the lines in
[The `~/.tmux.conf` lines](#the-tmuxconf-lines) to `~/.tmux.conf` by hand.

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
- `@continuum-boot 'on'` (operator answer 47 b): continuum writes and **enables** a systemd
  user unit, `~/.config/systemd/user/tmux.service`, that starts a detached tmux server at boot,
  so the sessions are back before you log in. Its `ExecStop` runs a resurrect save, then
  `tmux kill-server`, so a clean shutdown saves. continuum writes the unit only when it does not
  exist yet, and only enables it: it does **not** start it, so the tmux server you are using
  when you set this up is not the unit's (see
  [Put the tmux server under the unit](#put-the-tmux-server-under-the-unit)). A user unit starts
  at boot only when lingering is enabled for your user (`loginctl enable-linger`), otherwise at
  your first login. On hooper linger is enabled (by hooper's opencode install), so the unit starts at
  boot. tmux-continuum owns the unit; hooper ships none.
- `@resurrect-capture-pane-contents 'on'`: restore what was on screen in each pane. It breaks
  if a pane's `default-command` contains `&&` or `||` (the operator's config sets none).

### Optional: the sparse-restore notice in the status line

When `--pin` finds that the save being restored has markedly fewer sandbox panes than earlier
tmux servers ended with (see [Choosing an earlier save](#choosing-an-earlier-save)), it sets the
tmux option `@claude-sandbox-notice` until the first `claude-sandbox` command you type inside
tmux prints and clears it. To see it the moment you attach after a boot, add the element to
your status line **before** continuum's `run-shell` line. If your config sets `status-right`
itself, put it at the end of that value, so every `tmux source-file` sets the whole line again.
If it never does, start from tmux's current value: read it with `tmux show -gv status-right` and
paste that in place of `<your status-right>` (a config that never sets it gets tmux's default),
leaving out continuum's `#(…continuum_save.sh)` element if it shows, since continuum adds it
again:

```
set -g status-right '<your status-right>#{?@claude-sandbox-notice, #[reverse] #{@claude-sandbox-notice} #[default],}'
```

The shorter `set -ga status-right '#{?@claude-sandbox-notice, …}'` (append) also works, but each
`tmux source-file` appends another copy, so the notice shows twice after one reload, three times
after two; a new server starts with one.

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
systemctl --user is-active tmux.service         # active: the unit runs a tmux server
systemctl --user show -p MainPID --value tmux.service; tmux display -p '#{pid}'   # the same pid: your server IS the unit's
```

If the last two checks fail — the unit is inactive, or its pid differs from your server's —
your tmux server was started outside the unit. **Do not run the drills or Procedure B until it
is under the unit**: `systemctl --user stop`/`start` act on the unit, not on your server, and a
`systemctl --user start` while your own server runs is expected to kill that server (below).

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

### Put the tmux server under the unit

Once, after setting up, when the checks above show your server is not the unit's:

1. Save your work in every pane that is not a sandbox (editors, shells running jobs): step 3
   ends every program in the server. The sandbox containers keep running.
2. Save: `prefix + C-s`, wait for "Tmux environment saved!", and check `--list` as above.
3. From a terminal **outside tmux** (a plain terminal window, or an ssh session not in tmux):
   - If `is-active` said `inactive`: `tmux kill-server`. The unit is not active, so this is the
     plain kill it looks like.
   - If it said anything but `active` (`failed`, `activating`, ...): `systemctl --user stop
     tmux.service` first, then `tmux kill-server` for your own server.
   - If it said `active` but the pids differ: the unit already runs a server, and a plain
     `tmux kill-server` here is the trap in
     [Stopping and restarting tmux](#stopping-and-restarting-tmux) whenever the server it ends
     is the unit's (systemd then runs `ExecStop` and its save is empty). The likeliest cause of
     differing pids is two sockets: the unit runs in systemd's environment, without your
     shell's `TMUX_TMPDIR`, `-L` or `-S`, so it is a different server from yours. Run
     `systemctl --user stop tmux.service`. Its `ExecStop` saves the UNIT's server and moves
     `last` there. If your own server is still running afterwards, save again in it (`prefix +
     C-s`, which points `last` back at your layout), check `--list`, and only then
     `tmux kill-server` it. Then continue from step 4. If the pids still differ after step 5,
     stop and report: repeating steps 3-5 loops. Make the unit and your shell use one socket
     first (for example `systemctl --user set-environment TMUX_TMPDIR=...` or a drop-in
     for the unit).
4. From the same terminal, first run `claude-sandbox tmux restore --list`: if `last` is
   neither step 2's save nor your re-save from step 3 (a stop saved the unit's server),
   repoint it at the one you want as
   [Procedure B](#restore-a-whole-layout-from-an-earlier-save) does. Then
   `systemctl --user start tmux.service` and `tmux attach`. continuum restores that save, and
   each sandbox pane reattaches. If the first attach shows an empty session with one bash pane,
   or prints `[exited]`, the restore is still running (tmux-resurrect kills the default session
   `0` when it restores from scratch, and that is what the `[exited]` was). Wait until `tmux ls`
   lists the saved sessions and not only session `0`, then attach again. A restore with many
   panes or saved pane contents can take longer than a few seconds.
5. Run the checks above again: `is-active` says `active` and the two pids match.

Why it matters (expected from systemd's rules for a `Type=forking` unit, not yet observed — see
[Host checks still owed](#host-checks-still-owed)): with your own server running,
`systemctl --user start tmux.service` runs `tmux new-session -d`, which only adds a session to
that server and exits; nothing is left in the unit, so systemd considers it stopped and runs
its `ExecStop`, whose `tmux kill-server` ends your server.

### Stopping and restarting tmux

While the unit runs your server, stop tmux only with `systemctl --user stop tmux.service`, from a
terminal outside tmux (it ends every pane, the one you type it in included), and start it with
`systemctl --user start tmux.service`. **Never use a plain `tmux kill-server` then**: systemd is
expected to run the unit's `ExecStop` after the server is gone, and resurrect's save with no
server writes an empty save and points `last` at it. No sidecar is written for it, so the next
start restores an empty layout and `--pin` raises no notice (a save without a record is never
sparse). Expected from systemd's and resurrect's behaviour, not yet observed (host check below).

If it happened: `claude-sandbox tmux restore --list` shows the newest save as `no record`; pick
the save before it and use [Procedure B](#restore-a-whole-layout-from-an-earlier-save) (the unit
is already stopped, so its first line does nothing).

hooper's safe-shutdown text agrees: `systemctl --user stop tmux.service` from outside tmux
whenever the unit runs the server; without the unit, save first (`tmux set -g @continuum-save-interval 0`,
`prefix + C-s`) and then `tmux kill-server`; if the unit is active but the pids differ, follow step 3 of
[Put the tmux server under the unit](#put-the-tmux-server-under-the-unit).

## How a restore decides

For each pane, `claude-sandbox tmux restore` reads the pane's own pending or crashed mark, else
the row the save holds at the pane's coordinates (session, window, pane). A **crashed** row is
decided at once: the hint line is printed, the pane's crashed mark is removed, and nothing is
checked, locked or started (decision row 19). Any other row marks the pane pending before
anything else, so a restore cut short anywhere leaves it on the list. Then:

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
most 60 s), plus a gap: none when `CLAUDE_CONFIG_DIR` is set, or when `~/.claude.json` is linked
and there is no `~/.claude/.config.json`; 10 s otherwise. In the legacy layout several claudes
starting at once can overwrite each other's writes to that one file;
`claude-sandbox global-config migrate` (see the README's
[Global config](../README.md#global-config-claudejson)) removes the cause, and the 10 s with it.
A `~/.claude/.config.json` (which Claude Code prefers) keeps the 10 s even after a migrate.

## Drills

Run these once after setting up, and again after a change to the tmux config. **First make
sure your server is the unit's** ([Verify the wiring](#verify-the-wiring)); until it is, the
`systemctl` steps below act on nothing or kill your server. Type every `systemctl` line from a
terminal outside tmux: stopping the unit ends every pane.

**Restart drill** (no reboot; the sandboxes keep running, other programs in tmux end):

1. With a few sandbox sessions running, save (`prefix + C-s`) and check `--list` as above.
2. `systemctl --user stop tmux.service`. The containers keep running: they were created with a
   TTY, and losing the terminal does not stop them.
3. `systemctl --user start tmux.service`, then `tmux attach`.
4. Each sandbox pane prints its restore and reattaches, one at a time. `claude-sandbox
   sessions` shows the same containers as before.

Without the unit (no `@continuum-boot`), steps 2 and 3 are `tmux kill-server` and `tmux`.

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

**Crash drill** (a throwaway sandbox; nothing is relaunched):

1. In a pane, start a sandbox and talk to it for over a minute (so the save hook records its
   conversation), then kill its claude from another terminal: `docker exec <container> kill -9
   <claude pid>` reads as a crash, as an OOM kill does. `tmux show-options -p -v -t <pane>
   @claude-sandbox` shows `"state":"crashed"` with its `exitCode`.
2. Save (`prefix + C-s`), then `systemctl --user stop tmux.service` and `start` it (or
   `tmux kill-server` and `tmux`).
3. The pane comes back at its folder with the typed `claude-sandbox tmux restore --resurrected`
   and one hint line; no container starts (`claude-sandbox sessions`).
4. Run the hint's command: it resumes the conversation.

**Sparse-save drill** (shows the warning and the recovery; at least 2 sandbox sessions
running, throwaway ones if you prefer):

1. In `~/.tmux.conf`, comment out the `@resurrect-hook-pre-restore-all` and
   `@resurrect-processes` lines, then (outside tmux) `systemctl --user stop tmux.service` and
   `start` it. The
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
6. If a session still does not come back, its container is still running: from its project
   directory, `claude-sandbox --attach=<noun>` (`claude-sandbox sessions` lists the nouns).

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
tmux source-file ~/.tmux.conf               # puts back the interval your config sets
```

resurrect creates only panes that do not exist yet. A pane that already exists at a saved
coordinate is left alone; arm its row with `--all --from <stamp>` (below) or type
`claude-sandbox tmux restore --from <stamp>` in it. With `renumber-windows on`, existing
windows may occupy saved indices: `claude-sandbox tmux restore --dry-run --all --from <stamp>`
first shows which rows land where.

**B, with a fresh tmux server** (needs the unit to run your server, see
[Verify the wiring](#verify-the-wiring); type it from a terminal **outside tmux** — the first line
ends every pane, the one it is pasted into included):

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

### Drop a pending or crashed pane: `--drop`

A pending mark is carried from save to save until a restore decides it, and so is a crashed
one until its hint is shown. If you do not want the session back, or the hint, type
`claude-sandbox tmux restore --drop` in the pane: it removes the pending or crashed mark and
restores nothing. Closing the window works too.

A pane holding a crashed mark refuses `claude-sandbox tmux restore --from <save>` (exit 2): an
older row there is usually the same session before its crash. Run `--drop` first if you do
want that save's row in the pane. `--all` never arms a crashed row, nor a pane holding a
crashed mark; it prints the hint in its listing instead.

## Typing safety

claude-sandbox types into a pane in two cases: `--rearm` retyping the restore into a pane that
was pending or crashed at a bare shell when saved (resurrect typed nothing there; a crashed
pane's restore only prints its hint), and `--all` arming a pane. In both, the keys go in only when the pane is provably idle at its own shell's prompt:

- its current command is tmux's `default-shell`;
- the pane's own shell leads its terminal's foreground process group (so a running script, a
  program started from a wrapper, or a `su -` root shell does not count);
- it is not in copy mode and its window is not synchronized;
- no client is looking at it, through any session, read again right before the keys.

The saved directory is checked differently:

- **`--all`** types only when the saved directory could be compared with where the pane is and
  matched. A pane whose saved directory is empty, or whose current path holds any whitespace
  other than a plain space, a run of whitespace, or whitespace at either end (which
  resurrect's save cannot record), is only marked.
- **`--rearm`** never touches a pane that is somewhere other than its saved directory. When the
  directory cannot be compared (an empty saved directory, or such a current path) it arms and
  types on the other checks alone: the pane was just created by this restore at that place.

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

With `@continuum-boot 'on'` and your server under the unit, in this order:

0. If you pulled the claude-sandbox checkout since the last launch, run any `claude-sandbox`
   command first (`claude-sandbox tmux restore --list` will do): it rebuilds the binary, and
   until then the save hook writes nothing.
1. Let each session finish its turn. Do not `/exit` the sessions you want back: a clean exit
   removes the pane's mark, and the next save drops the session.
2. Optional: `tmux set -g @continuum-save-interval 0` and wait a few seconds, so no autosave
   runs alongside the shutdown save.
3. Save while the sandboxes still run: `prefix + C-s`, and wait for "Tmux environment saved!".
4. `claude-sandbox tmux restore --list`: the newest run holds every sandbox pane, active. If it
   says `no record`, the save hook did not run: check `~/.cache/claude-sandbox/tmux-save.log`,
   run step 0, and save again.
5. Reboot. Do not stop tmux first, and never with `tmux kill-server` (see
   [Stopping and restarting tmux](#stopping-and-restarting-tmux)): the unit's `ExecStop` saves
   once more on the way down.

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

Stopping tmux without rebooting stops no sandbox: the containers run on, and a restore (or
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
- **`tmux kill-server` with the unit active.** Expected to leave `last` on an empty save; see
  [Stopping and restarting tmux](#stopping-and-restarting-tmux) for the recovery.
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
window gets its automatic name back, unless the pane stays pending or crashed. A restore reclaims
a window it named before only when the saved row records that claude-sandbox named it; a
crashed row's hint reclaims it the same way (never renaming it), so the next launch in the
pane owns the name again. Every `#` is
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

These behaviours the design relies on have not been observed on the host yet. Record each
result in `spec/tmux.feature`, in the comment of the scenario it concerns (named per item).
Use throwaway sessions and, where noted, a scratch `CLAUDE_CONFIG_DIR` (never the real one).

- [ ] **`claude --resume` record timing** (CS-TMUX-061). With a scratch `CLAUDE_CONFIG_DIR`,
  run `claude --resume <id>` in one terminal and `watch -n0.2 ls -l <config dir>/sessions/` in
  another. *Pass:* a `<pid>.json` appears before (or with) the first interactive screen, and soon
  names `<id>`. *Fail:* no record until you answer a prompt — a restore then counts as "up" only
  after its 5 s fallback, so each pane costs up to 5 s plus the gap; nothing else changes.
- [ ] **docker events at daemon shutdown** (CS-TMUX-071). **`sudo systemctl stop docker` stops
  every container on the host:** live sandboxes are `--rm`, so they are removed, and ralph loops
  end. Exit every real session first (or skip this item). With one throwaway sandbox running, run
  `docker events --filter type=container` in another terminal while you run `docker stop`, then
  (relaunched) `docker kill`, then `sudo systemctl stop docker`. *Pass:* each shows a `kill` or
  `stop` event before the `die`. *Fail* for the daemon shutdown: F1b then relies on the
  `stopping` check below and on the save hook's pending rows.
- [ ] **`systemctl is-system-running` during a shutdown** (CS-TMUX-071). Create
  `~/.config/systemd/user/state-probe.service` with `[Service]` `Type=oneshot`,
  `RemainAfterExit=yes`, `ExecStart=/bin/true`,
  `ExecStop=/bin/sh -c 'systemctl is-system-running >> %h/state-probe.log'` and `[Install]`
  `WantedBy=default.target`; `systemctl --user daemon-reload && systemctl --user enable --now
  state-probe.service`; reboot; read `~/state-probe.log` (without linger each logout also
  writes a line, so read the one from the reboot, the last before the boot), then `disable`
  and remove the unit.
  *Pass:* it says `stopping`. *Fail:* the launcher's shutdown check never fires; F1b relies on
  docker's events alone.
- [ ] **docker client exit status on a detach** (CS-TMUX-071). Known from the docker/cli source
  only: `docker start -ai` exits 0 on the detach keys, `docker attach` exits 1. The launcher hides
  the status, so try it on a throwaway container by hand:
  `docker create -it --name probe debian:bookworm-slim bash`, then
  `docker start -ai --detach-keys=ctrl-q,ctrl-q probe`, detach, `echo $?`; then
  `docker attach --detach-keys=ctrl-q,ctrl-q probe`, detach, `echo $?`; `docker rm -f probe`.
  *Pass:* 0, then 1. *Fail:* a real detach can read as an inconclusive end and leave its pane
  pending (harmless: the next restore attaches; `--drop` clears it).
- [ ] **claude's exit codes** (CS-TMUX-071/075) for `/exit`, Ctrl-D, a double Ctrl-C and SIGTERM
  (`kill <pid>` from another terminal): run host `claude` in a scratch directory, end it each
  way, `echo $?` each time. *Pass:* `/exit` and Ctrl-D print 0. *Fail:* a clean end that
  exits non-zero leaves a crashed mark, and the next restore prints one stale hint line for
  that pane (nothing is ever relaunched); add the code to `tmuxpane.CleanExitCodes` (today 0
  and 78).
- [ ] **bidi characters in existing saves** (CS-TMUX-046): the scan in that scenario's comment
  counts sidecar rows whose values hold one of the twelve Bidi_Control characters, which a
  restore now clears instead of resuming. *Pass:* 0 rows.
- [ ] **tpgid at a prompt** (CS-TMUX-067). In a pane at a prompt, from another pane:
  `p=$(tmux display -p -t <pane> '#{pane_pid}'); sed 's/.*) //' /proc/$p/stat | awk '{print $6}'`.
  *Pass:* it prints `$p`; with `sleep 30` running in that pane it prints another number. *Fail:*
  every pane reads as busy and is only marked, never typed into.
- [ ] **darwin** (CS-TMUX-067): `ps -o tpgid= -p <pane pid>` gives the same two answers on macOS.
  *Fail:* the same as above, on macOS.
- [ ] **tmux 3.5a formats** (CS-TMUX-020..026). `tmux display -p '#{automatic-rename}'` in a
  window tmux names itself. *Pass:* `1` or `on`; *fail:* windows are never labelled.
  `tmux rename-window -t <scratch window> '#(echo expanded)'`. *Pass:* the window is named
  `expanded` (the reason claude-sandbox removes `#`); *fail:* harmless. With the status element
  added, `tmux set -g @claude-sandbox-notice test`, look, then `tmux set -gu
  @claude-sandbox-notice`. *Pass:* `test` shows next to continuum's part of the line.
- [ ] **resurrect save and restore of a labelled window** (CS-TMUX-022). Launch a sandbox in a
  scratch window, save, run the restart drill. *Pass:* the window comes back with the same name,
  and after its restore `tmux show-options -w` shows `@claude-sandbox-label` again. *Fail:* the
  window keeps its restored name but is not refreshed by later `/rename`s.
- [ ] **The unit and a server started outside it** (this guide). Only with nothing open you
  need: with the unit inactive and your own server running,
  `systemctl --user start tmux.service`. This cannot be done in a throwaway tmux server: the
  unit acts on the default socket, which is your own server, and its `ExecStop` save is a save
  of that live server (a good save: the live layout plus the session `new-session -d` adds) and
  moves `last` to it, which is fine; use Procedure B only if you want the earlier save.
  *Expected:* your server is killed. *If not:* the
  warning in [Verify the wiring](#verify-the-wiring) can be softened.
- [ ] **`tmux kill-server` with the unit active** (this guide). After a good save, with nothing
  open you need: `tmux kill-server`, then `ls -l <dir>/last` and `wc -c` of its target.
  *Expected:* `last` points at a new, empty save, and `--list` shows it as `no record`; repair
  with Procedure B. *If not:* the warning in
  [Stopping and restarting tmux](#stopping-and-restarting-tmux) can be softened.

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
