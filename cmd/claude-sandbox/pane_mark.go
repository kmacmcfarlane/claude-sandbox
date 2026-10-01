package main

// The tmux pane mark (CS-TMUX-010..019, CS-TMUX-071, plan
// sandbox-reboot-restore 05..13): every host launch, attach or join run inside
// tmux records in a pane user option which sandbox the pane holds, so the save
// hook (F3) and `claude-sandbox tmux restore` (F4) can bring it back after a
// tmux server restart or a reboot. runSession sets it before the session child
// starts and, when the child returns, removes it — or keeps it pending when
// the session was stopped from outside (F1b). It never changes the launch.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// paneMark is what runSession writes to the pane, and how it undoes it.
type paneMark struct {
	next tmuxpane.Mark
	// prior is the raw mark to put back when the start fails (CS-TMUX-018).
	// nil means read the pane's own before setting — a hand launch; a restore
	// (F4) hands in its own pending mark and gets no note.
	prior *string
	// restoreAttach marks a restore-initiated attach (CS-TMUX-019): a
	// container that vanished between the restore's inspect and the attach
	// puts the prior mark back.
	restoreAttach bool
	// resuming is the conversation this launch resumes explicitly, so a
	// pending mark naming it gets no note (CS-TMUX-017).
	resuming string
}

// markedPane is a pane runSession marked.
type markedPane struct {
	pane  tmuxpane.Pane
	prior string
	// own is the mark this launch set: a pending write-back flips only it
	// (CS-TMUX-071).
	own tmuxpane.Mark
	// decided is set once the end is known without CS-TMUX-071's checks.
	decided bool
}

// beginMark marks the pane before the session child starts (CS-TMUX-010).
// nil when no mark applies: no mark asked for, headless, not in tmux, or in a
// sandbox (CS-TMUX-014).
func beginMark(env *Env, o sessionOpts) *markedPane {
	if o.mark == nil || o.headless {
		return nil
	}
	id, ok := tmuxpane.FromEnv(env.Getenv)
	if !ok {
		return nil
	}
	m := &markedPane{pane: tmuxpane.Pane{Runner: env.Runner, ID: id}, own: o.mark.next}
	if o.mark.prior != nil {
		m.prior = *o.mark.prior
	} else {
		m.prior = m.pane.Read()
		if note := tmuxpane.PendingNote(m.prior, o.mark.resuming); note != "" {
			fmt.Fprintln(env.Err, note)
		}
	}
	if _, ok := tmuxpane.ParseMark(m.prior); !ok {
		m.prior = "" // CS-TMUX-016: an unparsable mark is no mark
	}
	m.pane.Set(o.mark.next.JSON())
	return m
}

// markEnd is what happens to the pane's mark when the session ends.
type markEnd int

const (
	// markUnset removes it (CS-TMUX-015).
	markUnset markEnd = iota
	// markPutBack sets the prior mark back: the session never really
	// started (CS-TMUX-018/019).
	markPutBack
	// markPending keeps this session's mark, as pending: it was stopped
	// from outside (CS-TMUX-071).
	markPending
)

// end settles the pane's mark (CS-TMUX-015/018/019/071).
func (m *markedPane) end(a markEnd) {
	if m == nil {
		return
	}
	switch a {
	case markPutBack:
		if m.prior != "" {
			m.pane.Set(m.prior)
			return
		}
	case markPending:
		if raw, ok := m.pendingMark(); ok {
			m.pane.Set(raw)
			return
		}
	}
	m.pane.Unset()
}

// pendingMark is the pane's current mark with state pending, when it is
// still this session's own (CS-TMUX-071): re-read, because the save hook
// writes the conversation into it while the session runs (CS-TMUX-035).
// Another mark, none, or a failed read gives false: the pane is unset.
func (m *markedPane) pendingMark() (string, bool) {
	cur, ok := tmuxpane.ParseMark(m.pane.Read())
	if !ok {
		return "", false
	}
	switch {
	case m.own.ContainerID != "":
		ok = cur.ContainerID == m.own.ContainerID
	case m.own.Container != "":
		ok = cur.Container == m.own.Container
	default:
		ok = false
	}
	if !ok {
		return "", false
	}
	cur.State = tmuxpane.StatePending
	return cur.JSON(), true
}

// stoppedFromOutside is rows 1 and 2 of CS-TMUX-071: a kill or stop event
// before the die, or the host shutting down. Always false for an unmarked
// session, which runs no probe (headless keeps CS-LNCH-091's prompt exit).
func (m *markedPane) stoppedFromOutside(env *Env, out oomreport.Outcome) bool {
	if m == nil || m.decided {
		return false
	}
	return out.OutsideStop || tmuxpane.SystemStopping(env.Runner)
}

// pendingAfter decides the mark of a session that ended without a signal
// from the launcher (CS-TMUX-071 rows 1, 2 and 4..9). The rows that give
// pending without a probe are checked first, so the probe runs only when the
// end would otherwise unset; the verdict is the table's in its order.
func (m *markedPane) pendingAfter(env *Env, o sessionOpts, w *oomreport.Watch, code int, out oomreport.Outcome, container string) bool {
	if m == nil || m.decided {
		return false
	}
	if out.OutsideStop {
		return true // row 1
	}
	if m.inconclusive(env, o, w, code, out, container) {
		return true // rows 6 and 9
	}
	return tmuxpane.SystemStopping(env.Runner) // row 2, else rows 4, 5, 7, 8: unset
}

// inconclusive is rows 4..9 of CS-TMUX-071: true for an end that gives no
// positive evidence the session ended or detached on its own.
func (m *markedPane) inconclusive(env *Env, o sessionOpts, w *oomreport.Watch, code int, out oomreport.Outcome, container string) bool {
	if out.Died {
		return false // row 4: a clean exit, a crash, an OOM kill
	}
	join := o.kind == joinedSession
	if join && code == 0 {
		return false // row 5: the joined claude ended on its own
	}
	if !w.StreamOpen() {
		return true // row 6: the daemon went away, or the subscription failed
	}
	target := container
	if m.own.ContainerID != "" {
		target = m.own.ContainerID
	}
	state, ok := tmuxpane.ContainerState(env.Runner, target)
	if !ok || (state != "running" && state != "paused") {
		return true // row 6
	}
	if o.kind != reservedSession {
		// Row 8: a join whose container runs on. Row 7, attach: "docker
		// attach" exits 1 on the detach keys (docker/cli RunAttach returns
		// the term.EscapeError), so for an attach no die + an open stream +
		// a running container is a detach whatever the exit code.
		return false
	}
	// Row 7, a new container: "docker start -ai" returns nil on the detach
	// keys (docker/cli start.go), so a detach exits 0; row 9 otherwise.
	return code != 0
}

// vanished reports a restore attach whose container was gone (CS-TMUX-019):
// docker attach failed within the die wait, and an inspect by id finds no
// container.
func vanished(env *Env, o sessionOpts, code int, started time.Time) bool {
	if o.mark == nil || !o.mark.restoreAttach || code == 0 || o.mark.next.ContainerID == "" {
		return false
	}
	if env.now().Sub(started) >= oomreport.DieWait {
		return false
	}
	state, _ := sessions.Inspect(env.Runner, o.mark.next.ContainerID)
	return state == ""
}

// launcherGiven names the launcher flags given on the command line that a
// restore may have to name (tmuxpane.Record filters them).
func (f *launchFlags) launcherGiven() []string {
	on := func(b *bool) bool { return b != nil && *b }
	var n []string
	if f.Dangerous {
		n = append(n, "--dangerous")
	}
	for _, x := range []struct {
		set  bool
		name string
	}{
		{on(f.SSH), "--ssh"}, {on(f.Git), "--git"}, {on(f.DockerSocket), "--docker-socket"},
		{on(f.AWS), "--aws"}, {on(f.PackageCaches), "--package-caches"},
	} {
		if x.set {
			n = append(n, x.name)
		}
	}
	return n
}

// launchRecord is this launch's replay record (CS-TMUX-013).
func launchRecord(env *Env, f *launchFlags) tmuxpane.LaunchRecord {
	return tmuxpane.Record(f.launcherGiven(), f.Model, f.Passthrough, env.Getenv)
}

// cwdRoot is where claude runs (plan 06 § 10 M6).
func cwdRoot(project, gitRoot, worktree string) string {
	if worktree != "" && gitRoot != "" {
		return filepath.Join(gitRoot, launch.WorktreeDir, worktree)
	}
	return project
}

// newContainerMark is the mark of a container this launch created
// (CS-TMUX-011). since was taken before the create.
func newContainerMark(plan *launch.Plan, gitRoot string, rec tmuxpane.LaunchRecord, since time.Time, resuming string) *paneMark {
	cde := plan.ConfigDirEnv
	mode := tmuxpane.ModeClaude
	if plan.Mode == sessions.ModeRalph {
		mode = tmuxpane.ModeRalph
	}
	return &paneMark{resuming: resuming, next: tmuxpane.Mark{
		V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: mode,
		Container: plan.ContainerName, ContainerID: plan.ContainerID,
		Instance: plan.Instance, Project: plan.ProjectDir,
		CwdRoot: cwdRoot(plan.ProjectDir, gitRoot, plan.Worktree),
		Class:   plan.PIDClass, Since: since.UnixMilli(),
		ConfigDir: plan.ConfigDir, ConfigDirEnv: &cde, RegistryDir: plan.RegistryDir,
		Worktree: plan.Worktree, Model: rec.Model,
		Replay: rec.Replay, Unreplayed: rec.Unreplayed,
	}}
}

// fullID is a 64-hex container id, "" for anything else (plan 07 § 1).
var fullID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// existingMark is the mark of an attach or a join to s (CS-TMUX-012): what the
// launch was is read from the container's CS-LNCH-109 labels.
func existingMark(s sessions.Session, mode, home, gitRoot, worktree string, since time.Time) tmuxpane.Mark {
	m := tmuxpane.Mark{
		V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: mode,
		Container: s.Name, Instance: s.Instance, Project: s.Project,
		CwdRoot: cwdRoot(s.Project, gitRoot, worktree),
		Class:   s.PIDClass, Worktree: worktree,
	}
	if fullID.MatchString(s.ID) {
		m.ContainerID = s.ID
	}
	if !since.IsZero() {
		m.Since = since.UnixMilli()
	}
	if s.RegistryDir == "" {
		// A container from before CS-LNCH-109: its config dir and launch
		// flags are unknown, and the save hook falls back.
		m.FlagsUnknown = true
		return m
	}
	cde := s.ConfigDirEnv
	m.ConfigDirEnv, m.RegistryDir = &cde, s.RegistryDir
	m.ConfigDir = cde
	if cde == "" && home != "" {
		m.ConfigDir = filepath.Join(home, ".claude")
	}
	return m
}

// attachMark is an attach's mark: the container's own mode, its creation
// time, and only names for the launch's flags — an attach never saw their
// values (CS-TMUX-012).
func attachMark(s sessions.Session, home, gitRoot string) *paneMark {
	mode := tmuxpane.ModeClaude
	if s.Mode == sessions.ModeRalph {
		mode = tmuxpane.ModeRalph
	}
	m := existingMark(s, mode, home, gitRoot, s.Worktree, s.CreatedAt)
	if !m.FlagsUnknown {
		modelGiven, unreplayed := tmuxpane.FromLabel(s.LaunchFlags)
		if modelGiven {
			m.Model = s.Model
		}
		m.Unreplayed = unreplayed
	}
	return &paneMark{next: m}
}

// joinMark is a join's mark: mode join, since the exec, and the join's own
// command line (CS-TMUX-012). Its worktree is recorded only when the mode
// resolved on: a --worktree=NAME that stood down runs in the shared checkout,
// and a bare --worktree lets claude generate a name the launcher never sees
// — unknown, never "" (the shared checkout).
func joinMark(s sessions.Session, home string, wt worktreeChoice, rec tmuxpane.LaunchRecord, since time.Time, resuming string) *paneMark {
	worktree := ""
	if wt.Enabled {
		worktree = wt.Name
	}
	m := existingMark(s, tmuxpane.ModeJoin, home, wt.Root, worktree, since)
	m.WorktreeGenerated = wt.Enabled && wt.Name == ""
	m.FlagsUnknown = false
	m.Model, m.Replay, m.Unreplayed = rec.Model, rec.Replay, rec.Unreplayed
	return &paneMark{next: m, resuming: resuming}
}
