package main

// The tmux pane mark (CS-TMUX-010..019, CS-TMUX-071, plan
// sandbox-reboot-restore 05..13): every host launch, attach or join run inside
// tmux records in a pane user option which sandbox the pane holds, so the save
// hook (F3) and `claude-sandbox tmux restore` (F4) can bring it back after a
// tmux server restart or a reboot. runSession sets it before the session child
// starts and, when the child returns, removes it — or keeps it pending when
// the session was stopped from outside (F1b), or crashed when it crashed
// (CS-TMUX-075, answer 64 c). It never changes the launch.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
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
	// keepUnlessReady, set only by a restore resume (CS-TMUX-062), reports
	// whether the session ended before it was resumed and within EarlyEnd:
	// the prior mark (the restore's pending row) then goes back — as
	// crashed when the end was a crash (answer 90 a), which onEarlyCrash
	// is then told.
	keepUnlessReady func() bool
	onEarlyCrash    func(crashEnd)
	// guardedResume is the conversation this launch resumes, as the resume
	// guard reads it ("" for a fork, which gets a new id): the new
	// container's claude-sandbox.resume label, an attach's container's. A
	// crash falls back to it when the mark names no conversation yet
	// (CS-TMUX-075).
	guardedResume string
	// name is the conversation name the launch gives claude (--name), the
	// window label's source (CS-TMUX-021); "" for none.
	name string
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
	// owned is true when this launch named the pane's window and owns the
	// label (CS-TMUX-020..023): only then does the end hand it back.
	owned bool
	// handed is the row a restore handed in as the prior (CS-TMUX-018), nil
	// for a hand launch; guarded is paneMark.guardedResume. Both feed a
	// crashed mark's conversation (CS-TMUX-075).
	handed  *tmuxpane.Mark
	guarded string
	// crash is how a crashed end ended (markCrashed, markCrashedPrior).
	crash crashEnd
}

// crashEnd is a crash's record in the mark (CS-TMUX-075).
type crashEnd struct {
	code int
	oom  bool
	at   time.Time
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
	m.guarded = o.mark.guardedResume
	if o.mark.prior != nil {
		m.prior = *o.mark.prior
		if h, ok := tmuxpane.ParseMark(m.prior); ok {
			m.handed = &h
		}
	} else {
		m.prior = m.pane.Read()
		if note := tmuxpane.PendingNote(m.prior, o.mark.resuming); note != "" {
			fmt.Fprintln(env.Err, note)
		}
	}
	if _, ok := tmuxpane.ParseMark(m.prior); !ok {
		m.prior = "" // CS-TMUX-016: an unparsable mark is no mark
	}
	// CS-TMUX-020..022: the window label first, so the mark records whether
	// this launch owns it (Labelled) — a later restore of the row reclaims
	// the window only then. The mark is still set before the session child.
	m.owned = m.pane.BeginLabel(tmuxpane.LaunchLabel(labelName(o.mark), o.mark.next.Project), reclaims(o.mark))
	m.own.Labelled = m.owned
	m.pane.Set(m.own.JSON())
	return m
}

// reclaims reports whether this launch may reclaim a restored window
// (CS-TMUX-022 case C): only a restore (which hands its row in as the prior)
// whose row records that its launch owned the label.
func reclaims(pm *paneMark) bool {
	if pm.prior == nil {
		return false
	}
	prior, ok := tmuxpane.ParseMark(*pm.prior)
	return ok && prior.Labelled
}

// labelName is the user-given name the window label takes (CS-TMUX-021):
// the launch's --name, else — for a restore, which hands in the pane's
// pending row — that row's name when the user gave it.
func labelName(pm *paneMark) string {
	if pm.name != "" || pm.prior == nil {
		return pm.name
	}
	if prior, ok := tmuxpane.ParseMark(*pm.prior); ok && prior.NameSource == tmuxpane.NameSourceUser {
		return prior.Name
	}
	return ""
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
	// markCrashed keeps this session's mark, as crashed: it died with a
	// crash exit code (CS-TMUX-075). A restore prints its hint and never
	// relaunches it.
	markCrashed
	// markCrashedPrior sets the prior (a restore resume's pending row) back
	// as crashed: the resume crashed before it was up (CS-TMUX-062).
	markCrashedPrior
)

// end settles the pane's mark (CS-TMUX-015/018/019/062/071/075). Only an
// unset mark — the session really ended — hands the window label back
// (CS-TMUX-024): a pane kept pending or crashed, or given its prior back, is
// waiting to be restored or to show its hint, and keeps its name.
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
	case markCrashed:
		if raw, ok := m.crashedMark(); ok {
			m.pane.Set(raw)
			return
		}
	case markCrashedPrior:
		if p := m.handed; p != nil && registry.IsUUID(p.Conversation) {
			c := *p
			m.crash.mark(&c)
			m.pane.Set(c.JSON())
			return
		}
		if m.prior != "" {
			m.pane.Set(m.prior)
			return
		}
	}
	alt := ""
	if m.owned {
		// The conversation's own label: a refresh cut between its rename and
		// its option write leaves it as the name (CS-TMUX-043).
		if cur, ok := tmuxpane.ParseMark(m.pane.Read()); ok && cur.NameSource == tmuxpane.NameSourceUser {
			alt = tmuxpane.Label(cur.Name)
		}
	}
	m.pane.Unset()
	if m.owned {
		m.pane.EndLabel(alt)
	}
}

// pendingMark is the pane's current mark with state pending, when it is
// still this session's own (CS-TMUX-071): re-read, because the save hook
// writes the conversation into it while the session runs (CS-TMUX-035).
// Another mark, none, or a failed read gives false: the pane is unset.
func (m *markedPane) pendingMark() (string, bool) {
	cur, ok := m.ownMark()
	if !ok {
		return "", false
	}
	cur.State = tmuxpane.StatePending
	return cur.JSON(), true
}

// ownMark re-reads the pane's mark; ok only while it is still this
// session's own (the same containerId, else the same container).
func (m *markedPane) ownMark() (tmuxpane.Mark, bool) {
	cur, ok := tmuxpane.ParseMark(m.pane.Read())
	if !ok {
		return cur, false
	}
	switch {
	case m.own.ContainerID != "":
		ok = cur.ContainerID == m.own.ContainerID
	case m.own.Container != "":
		ok = cur.Container == m.own.Container
	default:
		ok = false
	}
	return cur, ok
}

// crashedMark is the pane's current mark with state crashed and the crash's
// fields, when it is still this session's own and a conversation is known
// (CS-TMUX-075). The conversation is, in order: the re-read mark's own (the
// save hook writes it within a minute); else the row a restore handed in
// (a restore resume or a restore attach resumed exactly that id), with its
// user-given name when the mark has none; else the launch's guarded resume
// id — at once for an OOM kill, otherwise only once the session ran
// EarlyEnd from the mark's since, since a hand --resume of a missing id
// ("No conversation found") ends within seconds and must leave no hint.
// Anything else gives false: the pane is unset.
func (m *markedPane) crashedMark() (string, bool) {
	cur, ok := m.ownMark()
	if !ok {
		return "", false
	}
	switch {
	case registry.IsUUID(cur.Conversation):
	case m.handed != nil && registry.IsUUID(m.handed.Conversation):
		cur.Conversation = m.handed.Conversation
		if cur.Name == "" {
			cur.Name, cur.NameSource = m.handed.Name, m.handed.NameSource
		}
	case registry.IsUUID(m.guarded) &&
		(m.crash.oom || m.crash.at.UnixMilli()-m.own.Since >= tmuxpane.EarlyEnd.Milliseconds()):
		cur.Conversation = m.guarded
	default:
		return "", false
	}
	m.crash.mark(&cur)
	return cur.JSON(), true
}

// mark writes the crash into a mark (CS-TMUX-075).
func (c crashEnd) mark(m *tmuxpane.Mark) {
	m.State = tmuxpane.StateCrashed
	m.EndedAt, m.ExitCode, m.OOMKilled = c.at.UnixMilli(), c.code, c.oom
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

// endAfter decides the mark of a session that ended without a signal from
// the launcher (CS-TMUX-071 rows 1, 2 and 4..9, with row 4 split by
// CS-TMUX-075). The rows that give pending without a probe are checked
// first, so the probe runs only when the end would otherwise unset or be
// crashed; the verdict is the table's in its order. markCrashed may still
// fall back to an unset in end (no conversation known, another mark).
func (m *markedPane) endAfter(env *Env, o sessionOpts, w *oomreport.Watch, code int, out oomreport.Outcome, verdict oomreport.Verdict, container string) markEnd {
	if m == nil || m.decided {
		return markUnset
	}
	if out.OutsideStop {
		return markPending // row 1
	}
	if m.inconclusive(env, o, w, code, out, container) {
		return markPending // rows 6 and 9
	}
	if tmuxpane.SystemStopping(env.Runner) {
		return markPending // row 2
	}
	if m.crashed(env, o, out, verdict) {
		return markCrashed // row 4b
	}
	return markUnset // rows 4a, 4c, 5, 7, 8
}

// crashed is CS-TMUX-075's row 4b, before the conversation is known: the
// container died with a crash exit code, and the session is a claude-mode
// primary (a crashed join or ralph run unsets). It records the crash.
func (m *markedPane) crashed(env *Env, o sessionOpts, out oomreport.Outcome, verdict oomreport.Verdict) bool {
	if !out.Died || tmuxpane.CleanExit(out.ExitCode) || o.kind == joinedSession || m.own.Mode != tmuxpane.ModeClaude {
		return false
	}
	m.crash = crashEnd{code: out.ExitCode, oom: verdict == oomreport.Killed, at: env.now()}
	return true
}

// earlyEnd settles a restore resume that ended before it was up
// (CS-TMUX-062, answer 90 a): a die with a crash code, with no stop from
// outside and the host not shutting down, sets the pending row back as
// crashed; anything else puts it back as it was.
func (m *markedPane) earlyEnd(env *Env, o sessionOpts, out oomreport.Outcome, verdict oomreport.Verdict) markEnd {
	if m == nil || out.OutsideStop || !out.Died || tmuxpane.CleanExit(out.ExitCode) ||
		m.handed == nil || m.handed.Mode != tmuxpane.ModeClaude || !registry.IsUUID(m.handed.Conversation) {
		return markPutBack
	}
	if tmuxpane.SystemStopping(env.Runner) {
		return markPutBack
	}
	m.crash = crashEnd{code: out.ExitCode, oom: verdict == oomreport.Killed, at: env.now()}
	if o.mark.onEarlyCrash != nil {
		o.mark.onEarlyCrash(m.crash)
	}
	return markCrashedPrior
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
// guarded is the launch's resume label value (CS-LNCH-110).
func newContainerMark(plan *launch.Plan, gitRoot string, rec tmuxpane.LaunchRecord, since time.Time, resuming, guarded string) *paneMark {
	cde := plan.ConfigDirEnv
	mode := tmuxpane.ModeClaude
	if plan.Mode == sessions.ModeRalph {
		mode = tmuxpane.ModeRalph
	}
	return &paneMark{resuming: resuming, guardedResume: guarded, name: rec.Name, next: tmuxpane.Mark{
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
	// CS-TMUX-075: the container's resume label is the conversation a crash
	// falls back to.
	return &paneMark{next: m, guardedResume: s.Resume}
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
	return &paneMark{next: m, resuming: resuming, name: rec.Name}
}
