package main

// Restart a running session in a fresh container (CS-SESS-105..108,
// CS-LNCH-218/220/221, CS-TMUX-080; plan restart-session, first slice).
//
// "docker attach" is a raw TTY splice: the TUI redraws and the scroll-back is
// gone. A new claude started with --resume renders the conversation from its
// transcript. But two processes must never have one conversation open, so the
// old claude ends first, and ends cleanly: the restart reads the container's
// conversation from Claude Code's registry record at its pid class, refuses a
// busy turn, stops the container with docker stop --signal SIGTERM (the
// session-end helper's stop), waits until docker removed it, and then
// launches an ordinary new container that resumes the conversation in the
// same project, worktree and config dir, under the same noun. It is the tmux
// restore's resume (tmuxpane.ResumeArgs) typed by hand after a graceful stop.
//
// No resume-guard exemption: the stop runs after the image work and before
// the launch lock, so the guard under the lock finds nothing holding the
// conversation — or, when a launch slipped in between, refuses, and the
// restart prints the way back. Every failure after the stop prints the resume
// command and is logged to session-ends.jsonl.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessionend"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// The restart's waits (CS-LNCH-218). Variables so tests can shorten them.
var (
	// restartLockWait bounds the poll behind a session-end helper that holds
	// the container's end.lock (it may keep the container, waiting on a join
	// or a turn); nothing is stopped when it runs out.
	restartLockWait = 60 * time.Second
	// restartRemovalWait bounds the wait for docker to remove the stopped
	// --rm container, polled every restartRemovalPoll; past it the new
	// container takes a new noun (answer: reuse the noun, else a new one).
	restartRemovalWait = 10 * time.Second
	restartRemovalPoll = 250 * time.Millisecond
)

// Turn states, as a restart logs them.
const (
	turnIdle    = "idle"
	turnWaiting = "waiting"
	turnUnknown = "unknown"
)

// restartState is one restart: what the decision read, and what the stop
// and the launch need. It lives on the Env copy of the launch (Env.restart).
type restartState struct {
	target sessions.Session
	// mark describes the conversation to resume (tmuxpane.ResumeArgs and
	// ResumeCommand read it): the target's place, the conversation, its
	// user-given name, the carried --model and the replayed claude flags.
	mark tmuxpane.Mark
	// source is where the conversation came from: record, label or mark.
	source string
	home   string
	dirs   []string
	class  int
	domain string
	// primary is the primary claude's record at the decision; nil on the
	// no-record path (the operator answered yes to the question).
	primary *registry.Record
	// joins are the other live records at the decision: joined sessions,
	// killed with the container.
	joins []registry.Record
	// statuses is each live record's status at the decision, by record key.
	statuses map[string]string
	// explicitID is the conversation the user's --resume named ("" none);
	// userPass the user's passthrough without its session family.
	explicitID string
	userPass   []string
	// clientKeys are the attached clients at the decision.
	clientKeys map[string]bool
	// joinKeys are the joins counted so far (rs.joins), by record key.
	joinKeys  map[string]bool
	turnState string

	// After the decision.
	// carried are the access flags carried over (CS-SESS-107): every way back
	// the restart prints, logs or leaves as a notice passes them too.
	carried []string
	held    []tmuxpane.HeldMark
	stopped bool
	sig     <-chan os.Signal
	stopSig func()
	caught  os.Signal
	watcher *tmuxpane.Watcher
	newPlan *launch.Plan
}

// recordKey identifies one claude process's record across reads.
func recordKey(r registry.Record) string { return strconv.Itoa(r.PID) + "/" + r.ProcStart }

// who is the target as messages name it.
func (rs *restartState) who() string { return "'" + sessionLabel(rs.target) + "'" }

// convLabel is "'<name>', <id>" or "<id>" for the conversation.
func (rs *restartState) convLabel() string {
	if n := sessions.DisplayName(rs.mark.Name); n != "" {
		return "'" + n + "', " + rs.mark.Conversation
	}
	return rs.mark.Conversation
}

// validateRestart refuses what contradicts --restart (CS-SESS-105,
// CS-LNCH-221), before any docker call.
func validateRestart(env *Env, f *launchFlags) error {
	if !f.Restart {
		return nil
	}
	const picks = "--restart picks a running session and replaces its container"
	switch {
	case hostdirs.InSandbox(env.Getenv):
		// CS-LNCH-221: a nested launcher sees neither the host's leases nor
		// its processes, cannot reach the host's tmux, and takes a
		// container-private launch lock.
		return exitErr(2, "Error: --restart is not available inside a sandbox: from here the launcher cannot see who is attached to\n"+
			"       the session or reach the host's tmux. Run it from the host: cd <project> && claude-sandbox --restart=<noun>")
	case f.Ralph:
		return exitErr(2, "Error: --restart is not valid with --ralph: a ralph loop has no conversation to resume (relaunch it with --ralph)")
	case f.Attach:
		return exitErr(2, "Error: --restart conflicts with --attach: %s", picks)
	case f.Join:
		return exitErr(2, "Error: --restart conflicts with --join: %s", picks)
	case f.Branch:
		return exitErr(2, "Error: --restart conflicts with --branch: %s; to fork a conversation instead, use --branch alone", picks)
	case f.NewSession:
		return exitErr(2, "Error: --restart conflicts with --new: %s (it implies a new container)", picks)
	case f.NoSessionCheck:
		return exitErr(2, "Error: --restart conflicts with --no-session-check: %s", picks)
	case f.Detach:
		return exitErr(2, "Error: --restart is not valid with --detach: the point of a restart is a fresh screen in this terminal")
	case f.Worktree != nil:
		return exitErr(2, "Error: --restart takes no --worktree or --no-worktree: the conversation's transcript is in the session's own place, and the restart resumes it there")
	}
	return restartPassthroughRefusal(f.Passthrough)
}

// restartPassthroughRefusal refuses claude flags that select another
// conversation or another place (CS-SESS-105): a restart resumes the target's
// conversation by id, where its transcript is.
func restartPassthroughRefusal(pt []string) error {
	ca := tmuxpane.WalkClaudeArgs(pt)
	switch {
	case ca.Continue:
		return exitErr(2, "Error: a restart resumes the session's conversation by id; drop --continue")
	case ca.Fork:
		return exitErr(2, "Error: a restart resumes the session's conversation itself; to fork it, use --branch, or claude-sandbox -- --resume <id> --fork-session, instead of restarting")
	case ca.WorktreeGiven:
		return exitErr(2, "Error: a restart resumes the conversation in the session's own place; drop claude's -w/--worktree")
	case ca.ResumeGiven && ca.ResumeID == "":
		return exitErr(2, "Error: a restart resumes the session's conversation by id; --resume without an id (claude's picker) is not valid with it")
	}
	for _, t := range pt {
		if t == "--" {
			break
		}
		switch name, _, _ := strings.Cut(t, "="); name {
		case "--session-id", "--from-pr", "--teleport":
			return exitErr(2, "Error: a restart resumes the session's conversation by id; %s is not valid with it", name)
		}
	}
	return nil
}

// resolveRestartTarget picks the session to restart (CS-SESS-105): a named
// noun among this project's containers, else the only restartable one, else
// tier 2 (exit 3 without a terminal). Headless, ralph, paused containers and
// reservations are refused by name (CS-LNCH-221).
func resolveRestartTarget(env *Env, found []sessions.Session, target string) (sessions.Session, error) {
	if target != "" {
		if s, ok := sessions.ByInstance(found, target); ok {
			if err := restartable(s); err != nil {
				return sessions.Session{}, err
			}
		}
	}
	var cands []sessions.Session
	paused := 0
	for _, s := range sessions.Interactive(found) {
		if s.State == "paused" {
			paused++
			continue
		}
		cands = append(cands, s)
	}
	if len(cands) == 0 && target == "" {
		if paused > 0 {
			return sessions.Session{}, exitErr(2, "Error: no running session for this project to restart (%d paused: unpause it first with docker unpause).", paused)
		}
		return sessions.Session{}, exitErr(2, "Error: no running session for this project to restart.")
	}
	return resolveTarget(env, cands, target, "restart")
}

// restartable refuses the containers a restart never replaces (CS-LNCH-221).
func restartable(s sessions.Session) error {
	label := sessionLabel(s)
	switch {
	case s.Mode == sessions.ModeHeadless:
		return exitErr(2, "Error: '%s' is a headless container: its SDK client owns the session and resumes it itself; a restart is for a terminal session.", label)
	case s.Mode == sessions.ModeRalph:
		return exitErr(2, "Error: '%s' is a ralph loop: it has no conversation to resume (relaunch it with --ralph).", label)
	case s.Reserved():
		return exitErr(2, "Error: '%s' is still being started; retry once it is up.", label)
	case s.State == "paused":
		return exitErr(2, "Error: '%s' is paused: docker stop would not end its claude gracefully. Unpause it first (docker unpause %s).", label, s.Name)
	}
	return nil
}

// inspected is what the restart reads from the old container with one
// docker inspect: claude's command line (Config.Cmd, the launcher's own
// create argv, held by the daemon), its mounts and its environment.
type inspected struct {
	Cmd    []string
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	}
	Env []string
}

// inspectRestartTarget runs the one docker inspect of the target.
func inspectRestartTarget(env *Env, s sessions.Session) (inspected, error) {
	id := s.Name
	if fullID.MatchString(s.ID) {
		id = s.ID
	}
	var stderr strings.Builder
	out, err := tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}.Output(execx.Cmd{
		Name: "docker",
		Args: []string{"inspect", "--type", "container", "-f",
			"{{json .Config.Cmd}}\x1f{{json .Mounts}}\x1f{{json .Config.Env}}", id},
		Stderr: &stderr,
	})
	var in inspected
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return in, errors.New(msg)
	}
	f := strings.Split(strings.TrimRight(out, "\r\n"), "\x1f")
	if len(f) != 3 {
		return in, errors.New("unexpected docker inspect output")
	}
	if json.Unmarshal([]byte(f[0]), &in.Cmd) != nil || json.Unmarshal([]byte(f[1]), &in.Mounts) != nil ||
		json.Unmarshal([]byte(f[2]), &in.Env) != nil {
		return in, errors.New("unexpected docker inspect output")
	}
	return in, nil
}

// prepareRestart is the decision phase (CS-SESS-106..108): before any image
// work, it reads the target, finds its conversation, refuses what a restart
// must not do, asks when something else would be affected, and rewrites f
// into the launch that resumes the conversation. Nothing is stopped here.
func prepareRestart(env *Env, projectDir string, f *launchFlags, cfg *cascade.Config, wt worktreeChoice, target sessions.Session, tier1 bool) (*restartState, error) {
	if err := restartPassthroughRefusal(f.Passthrough); err != nil {
		return nil, err
	}
	if err := restartable(target); err != nil {
		return nil, err
	}
	_, _, _, home := hostIdentity(env.Getenv)
	rs := &restartState{target: target, home: home, class: -1,
		statuses: map[string]string{}, clientKeys: map[string]bool{}, joinKeys: map[string]bool{}}
	attach := attachCommand(projectDir, home, target.Instance)

	// CS-SESS-107: the config dir is where the transcript is. A container
	// from before the labels names neither it nor its flags.
	if target.RegistryDir == "" {
		return nil, exitErr(2, "Error: %s was started by an older launcher that did not record its config dir and flags, so it cannot be\n"+
			"       restarted safely. Attach to it (%s), or end it and resume its conversation by hand.", rs.who(), attach)
	}
	if mine := env.Getenv("CLAUDE_CONFIG_DIR"); target.ConfigDirEnv != mine {
		prefix := "env -u CLAUDE_CONFIG_DIR "
		if target.ConfigDirEnv != "" {
			prefix = "CLAUDE_CONFIG_DIR=" + tmuxpane.ShellQuote(target.ConfigDirEnv) + " "
		}
		if strings.IndexFunc(target.ConfigDirEnv, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			prefix = ""
		}
		return nil, exitErr(2, "Error: %s runs with another Claude config dir than this launcher, and its conversation is filed there.\n"+
			"       Restart it with that config dir: cd %s && %sclaude-sandbox --restart=%s",
			rs.who(), tmuxpane.ShellQuote(projectDir), prefix, target.Instance)
	}
	configDir := target.ConfigDirEnv
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}

	// CS-SESS-107: claude's command line, its mounts and environment.
	ins, err := inspectRestartTarget(env, target)
	if err != nil {
		return nil, exitErr(2, "Error: cannot read %s's command line (%s); nothing was stopped.", rs.who(), registry.Printable(err.Error()))
	}

	// CS-SESS-106: the primary claude's record, read with the guard's own
	// fail-closed reader.
	rs.dirs = resumeguard.RegistryDirs(target.RegistryDir, configDir, home)
	if c, err := strconv.Atoi(target.PIDClass); err == nil && c >= 0 && c <= 255 {
		rs.class = c
	}
	cannotTell := func(why string) error {
		return exitErr(2, "Error: cannot tell which conversation %s has open (%s);\n"+
			"       attach to it (%s) or end it yourself. Nothing was stopped.", rs.who(), registry.Printable(why), attach)
	}
	var recs []registry.Record
	if rs.class >= 0 {
		if recs, err = resumeguard.ReadClass(rs.dirs, rs.class, nil); err != nil {
			return nil, cannotTell(err.Error())
		}
	}
	if len(recs) > 0 {
		if rs.domain, err = (resumeguard.Check{ProcRoot: env.ProcRoot, MachineIDPath: env.MachineIDPath, GOOS: env.GOOS}).OwnDomain(); err != nil {
			return nil, cannotTell("this host's pid namespace: " + err.Error())
		}
	}
	primary, primaryOK := rs.pickPrimary(recs)
	if primaryOK {
		if _, alive, err := resumeguard.AliveRecord(env.Runner, env.ProcRoot, target.Name, []registry.Record{primary}); err == nil && !alive {
			// The primary cannot be dead in a running container: the
			// launcher never restarts a guess.
			return nil, cannotTell(fmt.Sprintf("its primary record, pid %d, is not a live process", primary.PID))
		}
		rs.primary = &primary
	}
	for _, r := range rs.live(recs) {
		if rs.primary != nil && r.PID == rs.primary.PID {
			continue
		}
		if _, alive, err := resumeguard.AliveRecord(env.Runner, env.ProcRoot, target.Name, []registry.Record{r}); err != nil || alive {
			rs.joinKeys[recordKey(r)] = true
			rs.joins = append(rs.joins, r)
		}
	}

	mark := existingMark(target, tmuxpane.ModeClaude, home, wt.Root, target.Worktree, target.CreatedAt)
	var waiting []string
	if rs.primary != nil {
		// CS-SESS-106: claude must run in the same cwd, since Claude Code
		// files transcripts by cwd.
		want := cwdRoot(target.Project, wt.Root, target.Worktree)
		if !sameDir(rs.primary.Cwd, want) {
			return nil, exitErr(2, "Error: %s's claude runs in %s, not in the session's own place (%s), so a restart could not\n"+
				"       resume its conversation there. Resume it by hand from that directory. Nothing was stopped.",
				rs.who(), registry.Printable(rs.primary.Cwd), registry.Printable(want))
		}
		mark.Conversation = strings.ToLower(rs.primary.SessionID)
		mark.Name, mark.NameSource = rs.primary.Name, rs.primary.NameSource
		rs.source = "record"

		// CS-SESS-108: the turn rule over every live record; R1 refuses a
		// running turn.
		v, counted, err := rs.turn(env).Read()
		switch v {
		case sessionend.VerdictUnreadable:
			return nil, cannotTell(fmt.Sprint(err))
		case sessionend.VerdictWait:
			return nil, exitErr(1, "%s is busy (a turn is running, or a session there is in a shell command); not restarted.\n"+
				"Retry when its turn has ended, or attach to it: %s", rs.who(), attach)
		case sessionend.VerdictNone:
			return nil, cannotTell("its registry records stopped being readable")
		}
		for _, r := range counted {
			rs.statuses[recordKey(r)] = r.Status
			if r.Status == registry.StatusWaiting {
				waiting = append(waiting, recordLabel(r))
			}
		}
		rs.turnState = turnIdle
		if len(waiting) > 0 {
			rs.turnState = turnWaiting
		}
	} else {
		// CS-SESS-106 / CS-SESS-108: no primary record.
		if age := env.now().Sub(target.CreatedAt); !target.CreatedAt.IsZero() && age < sessionend.RecordGrace {
			return nil, exitErr(1, "%s started %d s ago and its claude is not up yet; retry in a few seconds.", rs.who(), int(age.Seconds()))
		}
		conv, name, source := rs.noRecordConversation(env)
		if conv == "" {
			return nil, cannotTell(fmt.Sprintf("no registry record at its pid class %s", target.PIDClass))
		}
		mark.Conversation, mark.Name, mark.NameSource = conv, name, ""
		if name != "" {
			mark.NameSource = tmuxpane.NameSourceUser
		}
		rs.source = source
		rs.turnState = turnUnknown
	}

	// CS-SESS-105: an explicit --resume must name this conversation; one a
	// join holds is refused (a restart moves only a container's main
	// conversation).
	rs.explicitID = tmuxpane.GuardedResumeID(f.Passthrough)
	if id := rs.explicitID; id != "" && !strings.EqualFold(id, mark.Conversation) {
		for _, j := range rs.joins {
			if strings.EqualFold(j.SessionID, id) {
				return nil, exitErr(2, "Error: conversation %s is open in a session joined to %s (%s); a restart moves only a container's\n"+
					"       main conversation. End that joined session in its own terminal, then resume it. Nothing was stopped.",
					id, rs.who(), target.Name)
			}
		}
		return nil, exitErr(2, "Error: %s has conversation %s open, not %s. Nothing was stopped.", rs.who(), mark.Conversation, id)
	}

	// CS-SESS-107: the old launch's own claude flags and model.
	notes := rs.composeMark(env, f, cfg, &mark, ins)
	rs.mark = mark
	if !tmuxpane.PrintableMark(mark) || tmuxpane.ResumeCommand(mark) == "" {
		return nil, exitErr(2, "Error: a value of %s's resume command holds a control or bidi character, so it cannot be shown\n"+
			"       safely; resume it by hand. Nothing was stopped.", rs.who())
	}

	// CS-SESS-108: who else would be affected.
	pres := rs.presence(env)
	var asks []string
	if rs.primary == nil {
		asks = append(asks, rs.noRecordLines()...)
	}
	asks = append(asks, rs.clientLines(pres)...)
	for _, c := range pres.Terminal() {
		rs.clientKeys[clientKey(c)] = true
	}
	for _, c := range pres.Joins() {
		rs.clientKeys[clientKey(c)] = true
	}
	asks = append(asks, rs.joinLines(pres)...)
	for _, w := range waiting {
		asks = append(asks, fmt.Sprintf("%s is waiting for an answer to a prompt; restarting drops that prompt.", w))
	}
	if tier1 {
		// The tier-1 [r] reads the flags the menu came with: a worktree
		// flag is dropped, with a note when it named another place.
		if n := worktreeFlagNote(f, target); n != "" {
			notes = append([]string{n}, notes...)
		}
	}
	what := fmt.Sprintf("Restarting %s (%s): its container is stopped and the conversation resumes here with a fresh screen; anything else running in that container ends with it.", rs.who(), rs.convLabel())
	if len(asks) == 0 {
		// Answer: no confirmation in the clean case; one line says what happens.
		fmt.Fprintln(env.Err, what)
		for _, n := range notes {
			fmt.Fprintln(env.Err, n)
		}
	} else {
		for _, n := range notes {
			fmt.Fprintln(env.Err, n)
		}
		if err := rs.ask(env, append(asks, what), "Restart "+rs.who()+"?"); err != nil {
			return nil, err
		}
	}
	// The launch that resumes it: tmuxpane.ResumeArgs, scanned as a
	// command line, with this launch's own flags kept.
	rs.userPass = tmuxpane.StripSessionFamily(f.Passthrough)
	nf, err := scanLaunchArgs(tmuxpane.ResumeArgs(rs.mark))
	if err != nil {
		return nil, err
	}
	f.NewSession = true
	f.Worktree, f.WorktreeName = nf.Worktree, nf.WorktreeName
	if f.Model == "" {
		f.Model = nf.Model
	}
	f.Passthrough = rs.passthrough()
	return rs, nil
}

// passthrough is the new claude's arguments: ResumeArgs' "--resume <id>
// [--name n] <replay…>", then the user's own passthrough with its session
// family stripped (CS-SESS-107).
func (rs *restartState) passthrough() []string {
	nf, err := scanLaunchArgs(tmuxpane.ResumeArgs(rs.mark))
	if err != nil {
		return nil
	}
	return append(append([]string(nil), nf.Passthrough...), rs.userPass...)
}

// pickPrimary is CS-SESS-106's primary record: sessions.PickPrimary (the
// friendly names' rule) and, since the earliest record may be a join's when
// the primary has none, the save hook's PrimaryWindow from the creation.
func (rs *restartState) pickPrimary(recs []registry.Record) (registry.Record, bool) {
	r, ok := sessions.PickPrimary(recs, rs.class, rs.target.CreatedAt, rs.domain)
	if !ok {
		return r, false
	}
	if r.StartedAt > rs.target.CreatedAt.Truncate(time.Second).Add(tmuxpane.PrimaryWindow).UnixMilli() {
		return registry.Record{}, false
	}
	return r, true
}

// live is the container's records among recs: at the class, started at or
// after its creation, not the launcher's own.
func (rs *restartState) live(recs []registry.Record) []registry.Record {
	since := rs.target.CreatedAt.Truncate(time.Second).UnixMilli()
	var out []registry.Record
	for _, r := range recs {
		if r.PID%256 != rs.class || r.StartedAt < since || (rs.domain != "" && r.PIDDomain == rs.domain) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// turn is the session-end helper's turn rule over the target.
func (rs *restartState) turn(env *Env) *sessionend.Turn {
	id := rs.target.ID
	if !fullID.MatchString(id) {
		id = rs.target.Name
	}
	return sessionend.NewTurn(sessionend.TurnOptions{
		Runner: env.Runner, ProcRoot: env.ProcRoot, Container: id, Class: rs.class,
		Since: rs.target.CreatedAt.UnixMilli(), OwnDomain: rs.domain, RegistryDirs: rs.dirs,
		Clock: envClock{env},
	})
}

// noRecordConversation is the no-record path's conversation (CS-SESS-106):
// a tmux pane mark of the container that names one (the save hook writes it,
// following /resume and /clear) and passes ValidateRow, else the container's
// resume label (the id its claude was launched with).
func (rs *restartState) noRecordConversation(env *Env) (conv, name, source string) {
	for _, h := range tmuxpane.MarksFor(env.Runner, rs.target.ID, rs.target.Name) {
		if tmuxpane.ValidateRow(h.Mark) == "" && registry.IsUUID(h.Mark.Conversation) {
			n := ""
			if h.Mark.NameSource == tmuxpane.NameSourceUser {
				n = h.Mark.Name
			}
			return strings.ToLower(h.Mark.Conversation), n, "mark"
		}
	}
	if registry.IsUUID(rs.target.Resume) {
		return strings.ToLower(rs.target.Resume), "", "label"
	}
	return "", "", ""
}

// noRecordLines is the no-record question's text (CS-SESS-108).
func (rs *restartState) noRecordLines() []string {
	from := "it was launched with conversation " + rs.mark.Conversation
	if rs.source == "mark" {
		from = "its tmux pane last recorded conversation " + rs.mark.Conversation
	}
	if n := sessions.DisplayName(rs.mark.Name); n != "" {
		from += " ('" + n + "')"
	}
	lines := []string{
		rs.who() + " has no registry record, so the launcher cannot see whether a turn is running or which conversation is open now;",
		from + ". If /resume or /clear switched it since, the open one is stopped and " + rs.mark.Conversation + " is resumed instead.",
	}
	if p := rs.pickerCmd(); p != "" && tmuxpane.PrintableMark(rs.mark) {
		lines = append(lines, "Find the other with claude's picker:  "+p)
	} else {
		lines = append(lines, "Find the other with claude's picker in that session's directory.")
	}
	return lines
}

// presence is the target's clients (CS-SESS-098): held leases and the /proc
// scan of docker CLI processes. Off Linux the scan cannot run: cannot tell.
func (rs *restartState) presence(env *Env) clients.Presence {
	t := clients.Target{ID: rs.target.ID, Name: rs.target.Name}
	if env.goos() != "linux" {
		p := clients.Present(clients.Options{StateRoot: env.stateDir(), ProcRoot: env.ProcRoot, UID: -1}, t, nil)
		if p.Err == nil {
			p.Err = errors.New("this host has no /proc to scan for docker clients")
		}
		return p
	}
	return clients.Present(clients.Options{StateRoot: env.stateDir(), ProcRoot: env.ProcRoot, UID: -1}, t, []clients.Target{t})
}

// clientKey identifies one client across reads.
func clientKey(c clients.Client) string {
	if c.Proc != nil {
		return "pid:" + strconv.Itoa(c.Proc.PID)
	}
	return c.Lease
}

// clientLines name each terminal client (answer: kind, tmux pane, since) and
// a presence that cannot be told.
func (rs *restartState) clientLines(p clients.Presence) []string {
	var out []string
	for _, c := range p.Terminal() {
		out = append(out, rs.who()+" is attached in another terminal ("+describeClient(c)+"); restarting moves its conversation here and ends that terminal's session.")
	}
	if p.CannotTell() {
		out = append(out, "Cannot tell whether a terminal is attached to "+rs.who()+" ("+registry.Printable(p.Err.Error())+").")
	}
	return out
}

// describeClient is "attach, tmux pane %12, since 2026-10-28 14:02" or
// "docker attach, pid 4242".
func describeClient(c clients.Client) string {
	if c.Proc != nil {
		kind := string(c.Proc.Inv.Kind)
		if kind == "" {
			kind = "docker client"
		}
		return fmt.Sprintf("%s, pid %d", kind, c.Proc.PID)
	}
	parts := []string{string(c.Kind)}
	if parts[0] == "" {
		parts[0] = "a client"
	}
	if c.TmuxPane != "" {
		parts = append(parts, "tmux pane "+registry.Printable(c.TmuxPane))
	}
	if !c.Since.IsZero() {
		parts = append(parts, "since "+c.Since.Local().Format("2006-01-02 15:04"))
	}
	return strings.Join(parts, ", ")
}

// recordLabel names a record's session in a message: its cleaned name and
// id, or the id.
func recordLabel(r registry.Record) string {
	if n := sessions.DisplayName(r.Name); n != "" {
		return "'" + n + "' (" + r.SessionID + ")"
	}
	return r.SessionID
}

// joinLines name each joined session (answer: each join, and that it is
// SIGKILLed), from its record, and the join clients no record names.
func (rs *restartState) joinLines(p clients.Presence) []string {
	var out []string
	for _, j := range rs.joins {
		out = append(out, "A joined session runs there too: "+recordLabel(j)+". It is killed (SIGKILL) when the container stops; its resume command is printed and logged after the stop.")
	}
	if n := len(p.Joins()); n > 0 && len(rs.joins) == 0 {
		out = append(out, fmt.Sprintf("%d joined client(s) of %s run there (docker exec); they are killed (SIGKILL) when the container stops.", n, rs.who()))
	}
	return out
}

// ask prints lines and asks a yes/no question, default No (CS-SESS-108).
// No terminal: exit 3, a decision is needed. No, Enter or a timeout: exit 1,
// nothing stopped.
func (rs *restartState) ask(env *Env, lines []string, question string) error {
	for _, l := range lines {
		fmt.Fprintln(env.Err, l)
	}
	if !env.Prompter.Interactive() {
		return exitErr(exitDecisionRequired, "Error: restarting %s needs a confirmation and no terminal is attached; nothing was stopped.", rs.who())
	}
	switch strings.ToLower(strings.TrimSpace(env.Prompter.Ask("", question+" [y/N]:", promptTimeout))) {
	case "y", "yes":
		return nil
	}
	return exitErr(1, "Not restarted; %s keeps running. Attach to it: %s", rs.who(), attachCommand(rs.target.Project, rs.home, rs.target.Instance))
}

// worktreeFlagNote is the note for a worktree flag a tier-1 [r] drops: the
// conversation resumes in the target's place (plan 04 § 4's rule).
func worktreeFlagNote(f *launchFlags, s sessions.Session) string {
	if f.Worktree == nil {
		return ""
	}
	where := "the shared checkout"
	if s.Worktree != "" {
		where = "worktree '" + s.Worktree + "'"
	}
	var asked string
	switch {
	case !*f.Worktree:
		if s.Worktree == "" {
			return ""
		}
		asked = "--no-worktree"
	case f.WorktreeName != "":
		if f.WorktreeName == s.Worktree {
			return ""
		}
		asked = "--worktree=" + f.WorktreeName
	default:
		if s.Worktree != "" && s.Worktree == s.Instance {
			return ""
		}
		asked = "--worktree"
	}
	return fmt.Sprintf("Note: you asked for %s; %s runs in %s, so its conversation resumes there.", asked, "'"+sessionLabel(s)+"'", where)
}

// sameDir compares two directories lexically, else symlink-resolved.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// composeMark fills the resume mark from the old launch (CS-SESS-107) and
// carries its access options over (CS-SESS-107): it returns the notes to
// print, the carried options' line among them.
func (rs *restartState) composeMark(env *Env, f *launchFlags, cfg *cascade.Config, m *tmuxpane.Mark, ins inspected) []string {
	t := rs.target
	var notes []string
	// Config.Cmd is the launcher's own create argv: CommandPrefix, then the
	// passthrough. The labels say what the prefix holds.
	var pass []string
	prefixOK, prefixDangerous := false, false
	for _, dangerous := range []bool{true, false} {
		p := launch.CommandPrefix(dangerous, t.Worktree, t.Model)
		if len(ins.Cmd) >= len(p) && slices.Equal(ins.Cmd[:len(p)], p) {
			prefixOK, prefixDangerous, pass = true, dangerous, ins.Cmd[len(p):]
			break
		}
	}
	rec := tmuxpane.LaunchRecord{}
	if prefixOK {
		rec = tmuxpane.Record(nil, "", pass, nil)
		m.Replay = rec.Replay
	} else {
		m.FlagsUnknown = true
		notes = append(notes, "Note: "+rs.who()+"'s command line is not in the shape this launcher writes, so its claude flags are not replayed.")
	}
	// The model: the launcher's --model when the label says it was given,
	// else claude's own from the command line; this launch's --model wins.
	modelGiven, _ := tmuxpane.FromLabel(t.LaunchFlags)
	m.Model = ""
	switch {
	case f.Model != "":
	case modelGiven:
		m.Model = t.Model
	case rec.ClaudeModel:
		m.Model = rec.Model
	}
	// A user --name replaces the conversation's name (and is a user's).
	if n := tmuxpane.NameArg(f.Passthrough); n != "" {
		m.Name, m.NameSource = n, tmuxpane.NameSourceUser
	}
	// Claude flags a resume does not replay are named, never carried; a
	// --dangerously-skip-permissions given to claude after -- is one of them.
	if len(rec.Unreplayed) > 0 {
		notes = append(notes, "Note: "+rs.who()+" was also launched with "+strings.Join(rec.Unreplayed, ", ")+
			" (claude flags a restart does not replay); pass them again after -- to keep them.")
	}
	carried, dropped := carryAccess(env, f, cfg, t, ins, rs.home, prefixOK && prefixDangerous)
	if len(carried) > 0 {
		var parts []string
		for _, c := range carried {
			parts = append(parts, c.flag+" ("+c.how+")")
			rs.carried = append(rs.carried, c.flag)
		}
		notes = append(notes, "Carried over from "+rs.who()+": "+strings.Join(parts, ", ")+".")
	}
	if len(dropped) > 0 {
		notes = append(notes, "Note: "+rs.who()+" had "+strings.Join(dropped, ", ")+
			" from its config, which no longer gives it; pass it again to keep it.")
	}
	return notes
}

// carriedOption is one access option a restart re-grants (CS-SESS-107).
type carriedOption struct{ flag, how string }

// carryAccess carries the old session's access options over (CS-SESS-107),
// but only what the launcher itself recorded as given at the old launch: the
// claude-sandbox.launchflags label naming the launcher flag, or naming its
// CLAUDE_SANDBOX_* switch. Such an option that this launch would not give on
// its own is set as its flag on f (this launch's own flags already won) and
// returned, so the restart names it.
//
// Nothing is ever inferred from the container's mounts, environment or
// command line: an env file and a cascade config.yaml are session-writable,
// so a session could otherwise write its way to an access option. An option
// that evidence says was in effect while the label names neither its flag
// nor its switch came from the old launch's config; when today's config no
// longer gives it, it is returned in dropped, for a note, and never granted.
func carryAccess(env *Env, f *launchFlags, cfg *cascade.Config, s sessions.Session, ins inspected, home string, prefixDangerous bool) (carried []carriedOption, dropped []string) {
	_, named := tmuxpane.FromLabel(s.LaunchFlags)
	labelled := s.RegistryDir != ""
	// Evidence, for the note only.
	mounted := func(dest string) bool {
		for _, m := range ins.Mounts {
			if filepath.Clean(m.Destination) == dest {
				return true
			}
		}
		return false
	}
	cachesOn := func() bool {
		root := filepath.Join(home, launch.PackageCacheRoot)
		vars := launch.PackageCacheVars()
		for _, kv := range ins.Env {
			k, v, ok := strings.Cut(kv, "=")
			if d, known := vars[k]; ok && known && filepath.Clean(v) == filepath.Join(root, d) {
				return true
			}
		}
		return false
	}
	cascadeMounts := func(dest string) bool {
		for _, m := range cfg.Mounts {
			c := m.Container
			if c == "" {
				c = m.Host
			}
			if dest != "" && filepath.Clean(c) == dest {
				return true
			}
		}
		return false
	}
	on := func(cli *bool, envVar string, yaml *bool) bool {
		if cli != nil && *cli {
			return true
		}
		switch env.Getenv(envVar) {
		case "1", "true", "yes":
			return true
		}
		return yaml != nil && *yaml
	}
	decide := func(flag, envVar string, nowOn, evidence bool) (carry bool, how string) {
		switch {
		case nowOn:
			return false, ""
		case slices.Contains(named, flag):
			return true, "given as a flag at its launch"
		case slices.Contains(named, envVar):
			return true, envVar + " was set at its launch"
		case labelled && evidence:
			dropped = append(dropped, flag)
		}
		return false, ""
	}

	if ok, how := decide("--dangerous", "CLAUDE_SANDBOX_DANGEROUS", resolveDangerous(env, f, cfg), prefixDangerous); ok {
		f.Dangerous = true
		carried = append(carried, carriedOption{"--dangerous", how})
	}
	t := true
	ha := cfg.HostAccess
	for _, o := range []struct {
		flag, envVar string
		cli          **bool
		yaml         *bool
		evidence     bool
		dest         string
	}{
		{"--ssh", "CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", &f.SSH, ha.SSH.Enabled, mounted(filepath.Join(home, ".ssh")), filepath.Join(home, ".ssh")},
		{"--git", "CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED", &f.Git, ha.Git.Enabled, mounted(filepath.Join(home, ".gitconfig")), filepath.Join(home, ".gitconfig")},
		{"--docker-socket", "CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED", &f.DockerSocket, ha.DockerSocket.Enabled, mounted("/var/run/docker.sock"), "/var/run/docker.sock"},
		{"--aws", "CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED", &f.AWS, ha.AWS.Enabled, mounted(filepath.Join(home, ".aws")), filepath.Join(home, ".aws")},
		{"--package-caches", "CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED", &f.PackageCaches, ha.PackageCaches.Enabled, cachesOn(), ""},
	} {
		nowOn := on(*o.cli, o.envVar, o.yaml) || cascadeMounts(o.dest)
		if ok, how := decide(o.flag, o.envVar, nowOn, o.evidence); ok {
			*o.cli = &t
			carried = append(carried, carriedOption{o.flag, how})
		}
	}
	return carried, dropped
}

// beforeReserve runs right before the launch lock (CS-LNCH-218): end.lock,
// the re-check, the marks, docker stop and the removal wait. Everything up
// to the stop can still refuse with nothing stopped; from the stop on, a
// failure prints the way back (CS-LNCH-220).
func (rs *restartState) beforeReserve(env *Env, projectDir string, in *launch.Inputs) error {
	// CS-SESS-107's backstop: the new container lands in the target's
	// project, or nothing is stopped.
	if filepath.Clean(projectDir) != filepath.Clean(rs.target.Project) {
		return exitErr(2, "Error: internal: the restart would launch in %s, not in %s; nothing was stopped.", projectDir, rs.target.Project)
	}
	rs.sig, rs.stopSig = env.signalsForRestart()
	attach := attachCommand(rs.target.Project, rs.home, rs.target.Instance)
	interrupted := func() error {
		rs.stopSignals()
		return exitErr(128+signalNumber(rs.caught), "Interrupted; nothing was stopped: %s keeps running.", rs.who())
	}

	// end.lock: one ender per container. A session-end helper holding it
	// is polled, bounded; whatever it decided, the re-check reads again.
	id := rs.target.ID
	gone := false
	var lock *os.File
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	if clients.IsFullID(id) {
		deadline := env.now().Add(restartLockWait)
		said := false
		for {
			l, held, err := sessionend.TryEndLock(env.stateDir(), id)
			if err != nil {
				fmt.Fprintf(env.Err, "Warning: cannot take %s's end lock (%v); going on without it.\n", rs.who(), err)
				break
			}
			if !held {
				lock = l
				break
			}
			if !said {
				fmt.Fprintf(env.Err, "%s is being ended by another process (its terminal closed); waiting.\n", rs.who())
				said = true
			}
			if !env.now().Before(deadline) {
				return exitErr(1, "%s is still held by another process ending it (a session-end helper waiting for a join or a turn);\n"+
					"not restarted. Retry, or attach to it: %s", rs.who(), attach)
			}
			if !rs.sleep(sessionend.EndPoll) {
				return interrupted()
			}
			c, err := sessions.InspectContainer(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}, id)
			if sessionend.Gone(c, err) {
				gone = true
				break
			}
			if err == nil && c.State == "paused" {
				return exitErr(1, "%s was paused meanwhile; not restarted. Unpause it first (docker unpause %s).", rs.who(), rs.target.Name)
			}
		}
	}
	if !gone {
		var err error
		if gone, err = rs.recheck(env); err != nil {
			return err
		}
		// A conversation switch the re-check accepted: the launch resumes
		// the one open now.
		in.Passthrough = rs.passthrough()
		in.Resume = resumeLabel(in.Passthrough, false, false)
	}
	if rs.caughtNow() {
		return interrupted()
	}
	if gone {
		// It ended meanwhile (a session-end helper's stop, or on its own):
		// nothing to stop, but from here on the conversation is open nowhere,
		// so a failure prints the way back as after a stop.
		rs.stopped = true
		fmt.Fprintf(env.Err, "%s ended meanwhile; resuming its conversation.\n", rs.who())
	} else {
		// CS-TMUX-080: every pane mark naming the old container is unset,
		// so its launcher settles to unset rather than pending.
		rs.held = tmuxpane.MarksFor(env.Runner, rs.target.ID, rs.target.Name)
		tmuxpane.UnsetMarks(env.Runner, rs.held)

		rs.stopped = true
		var stderr strings.Builder
		target := id
		if !fullID.MatchString(target) {
			target = rs.target.Name
		}
		err := tmuxpane.BoundedRunner{R: env.Runner, Timeout: sessionend.StopBound}.Run(execx.Cmd{
			Name: "docker", Args: sessionend.StopArgs(target), Stderr: &stderr,
		})
		if err != nil {
			c, ierr := sessions.InspectContainer(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}, target)
			if !sessionend.Gone(c, ierr) {
				// Nothing was stopped: the marks go back, nothing is created.
				rs.stopped = false
				tmuxpane.PutBackMarks(env.Runner, rs.held)
				rs.stopSignals()
				state := c.State
				if ierr != nil {
					state = "unknown"
				}
				msg := strings.TrimSpace(stderr.String())
				if msg == "" {
					msg = err.Error()
				}
				return exitErr(1, "Error: docker stop of %s failed (%s); %s may still be running (state: %s). Nothing was created.\n"+
					"       Attach to it: %s", rs.who(), registry.Printable(msg), rs.who(), state, attach)
			}
		}
		// CS-LNCH-218: until docker removed it, so the guard never sees it
		// and the noun is free; past the bound the reservation picks a new
		// noun.
		for deadline := time.Now().Add(restartRemovalWait); ; {
			_, err := sessions.InspectContainer(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}, target)
			if errors.Is(err, sessions.ErrNoSuchContainer) {
				break
			}
			if !time.Now().Before(deadline) {
				break
			}
			rs.waitAfterStop(restartRemovalPoll)
		}
		fmt.Fprintf(env.Err, "Stopped %s (%s).\n", rs.who(), rs.target.Name)
	}
	if len(rs.joins) > 0 {
		fmt.Fprintln(env.Err, "Its joined sessions were killed with it; resume them with:")
		for _, j := range rs.joins {
			fmt.Fprintln(env.Err, "  "+rs.joinResume(j))
		}
	}
	if rs.caughtNow() {
		return rs.failAfterStop(env, exitErr(128+signalNumber(rs.caught), "Interrupted."), sessionend.OutcomeInterrupted)
	}
	return nil
}

// recheck re-reads everything right before the stop (CS-SESS-108,
// CS-LNCH-218), in plan 04 § 1's order: the container, the class's records
// (a malformed one fails closed before the turn rule runs), the primary and
// its conversation, the turn rule, the clients. gone means there is nothing
// left to stop.
func (rs *restartState) recheck(env *Env) (gone bool, err error) {
	attach := attachCommand(rs.target.Project, rs.home, rs.target.Instance)
	id := rs.target.ID
	if !fullID.MatchString(id) {
		id = rs.target.Name
	}
	c, ierr := sessions.InspectContainer(tmuxpane.BoundedRunner{R: env.Runner, Timeout: tmuxpane.ProbeTimeout}, id)
	switch {
	case sessionend.Gone(c, ierr):
		return true, nil
	case ierr != nil:
		return false, exitErr(2, "Error: cannot tell whether %s still runs (%s); nothing was stopped.", rs.who(), registry.Printable(ierr.Error()))
	case c.State == "paused":
		return false, exitErr(1, "%s was paused meanwhile; not restarted. Unpause it first (docker unpause %s).", rs.who(), rs.target.Name)
	}
	cannotTell := func(why string) error {
		return exitErr(2, "Error: cannot tell which conversation %s has open now (%s);\n"+
			"       attach to it (%s) or end it yourself. Nothing was stopped.", rs.who(), registry.Printable(why), attach)
	}
	var recs []registry.Record
	if rs.class >= 0 {
		if recs, err = resumeguard.ReadClass(rs.dirs, rs.class, nil); err != nil {
			return false, cannotTell(err.Error())
		}
	}
	if len(recs) > 0 && rs.domain == "" {
		rs.domain, _ = (resumeguard.Check{ProcRoot: env.ProcRoot, MachineIDPath: env.MachineIDPath, GOOS: env.GOOS}).OwnDomain()
	}
	var asks []string
	primary, ok := rs.pickPrimary(recs)
	switch {
	case ok:
		if _, alive, aerr := resumeguard.AliveRecord(env.Runner, env.ProcRoot, rs.target.Name, []registry.Record{primary}); aerr == nil && !alive {
			return false, cannotTell(fmt.Sprintf("its primary record, pid %d, is not a live process", primary.PID))
		}
		if !strings.EqualFold(primary.SessionID, rs.mark.Conversation) {
			if rs.explicitID != "" {
				return false, exitErr(2, "Error: %s switched to conversation %s since you chose it, not the %s you named. Nothing was stopped.",
					rs.who(), primary.SessionID, rs.explicitID)
			}
			asks = append(asks, fmt.Sprintf("%s switched to conversation %s since you chose it; the restart now resumes that one.", rs.who(), recordLabel(primary)))
			rs.mark.Conversation = strings.ToLower(primary.SessionID)
			rs.mark.Name, rs.mark.NameSource = primary.Name, primary.NameSource
			rs.source = "record"
		}
	case rs.primary != nil:
		return false, cannotTell("its primary record is gone while the container runs")
	}
	v, counted, terr := rs.turn(env).Read()
	switch v {
	case sessionend.VerdictUnreadable:
		return false, cannotTell(fmt.Sprint(terr))
	case sessionend.VerdictWait:
		return false, exitErr(1, "%s started a turn since you chose it; not restarted. Retry when it is idle, or attach to it: %s", rs.who(), attach)
	}
	for _, r := range counted {
		if r.Status == registry.StatusWaiting && rs.statuses[recordKey(r)] != registry.StatusWaiting {
			asks = append(asks, fmt.Sprintf("%s is waiting for an answer to a prompt now; restarting drops that prompt.", recordLabel(r)))
		}
		// A new join: not one the decision counted (its join set), nor the
		// primary then or now.
		if !rs.joinKeys[recordKey(r)] && (rs.primary == nil || r.PID != rs.primary.PID) && (!ok || r.PID != primary.PID) {
			rs.joinKeys[recordKey(r)] = true
			rs.joins = append(rs.joins, r)
			asks = append(asks, "A joined session started there since you chose it: "+recordLabel(r)+". It is killed (SIGKILL) when the container stops; its resume command is printed and logged after the stop.")
		}
		rs.statuses[recordKey(r)] = r.Status
	}
	if ok && rs.turnState == turnUnknown {
		rs.turnState = turnIdle
	}
	pres := rs.presence(env)
	for _, c := range append(pres.Terminal(), pres.Joins()...) {
		if !rs.clientKeys[clientKey(c)] {
			rs.clientKeys[clientKey(c)] = true
			if c.Kind == clients.KindJoin {
				asks = append(asks, "A joined client started since you chose it ("+describeClient(c)+"); it is killed (SIGKILL) when the container stops.")
			} else {
				asks = append(asks, rs.who()+" was attached in another terminal since you chose it ("+describeClient(c)+"); restarting ends that terminal's session.")
			}
		}
	}
	if len(asks) > 0 {
		if err := rs.ask(env, append(asks, fmt.Sprintf("Restarting stops its container and resumes %s here.", rs.convLabel())), "Restart "+rs.who()+" anyway?"); err != nil {
			return false, err
		}
	}
	return false, nil
}

// resumeCmd is the conversation's way back (CS-LNCH-220): ResumeCommand
// with the access flags the restart carried, so a way back never drops
// them.
func (rs *restartState) resumeCmd() string {
	args := tmuxpane.ResumeArgs(rs.mark)
	if args == nil {
		return ""
	}
	return tmuxpane.CommandLine(rs.mark, append(append([]string(nil), rs.carried...), args...))
}

// pickerCmd is claude's picker in the session's place, with the carried
// access flags.
func (rs *restartState) pickerCmd() string {
	args := tmuxpane.ResumePickerArgs(rs.mark)
	if args == nil {
		return ""
	}
	return tmuxpane.CommandLine(rs.mark, append(append([]string(nil), rs.carried...), args...))
}

// joinResume is a killed join's way back: its resume command, else where to
// resume it by hand.
func (rs *restartState) joinResume(j registry.Record) string {
	if c := sessionend.ResumeCommandWith(rs.mark, j, rs.carried); c != "" {
		return c
	}
	return fmt.Sprintf("%s: resume it by hand from %s with claude-sandbox --new -- --resume %s", recordLabel(j), registry.Printable(j.Cwd), j.SessionID)
}

// sessionsForLog is the logged form of the conversations the stop ended.
func (rs *restartState) sessionsForLog() []sessionend.Session {
	out := []sessionend.Session{{SessionID: rs.mark.Conversation, Name: rs.mark.Name, NameSource: rs.mark.NameSource,
		Kind: "primary", Resume: rs.resumeCmd()}}
	if rs.primary != nil {
		out[0].PID = rs.primary.PID
	}
	for _, j := range rs.joins {
		out = append(out, sessionend.Session{SessionID: j.SessionID, Name: j.Name, NameSource: j.NameSource,
			PID: j.PID, Kind: "join", Resume: sessionend.ResumeCommandWith(rs.mark, j, rs.carried)})
	}
	return out
}

// logOutcome appends the restart's line to session-ends.jsonl (CS-LNCH-220)
// and, for any outcome but restarted, writes a notice the next launch in the
// project prints once; joins killed by the stop get notice entries too.
func (rs *restartState) logOutcome(env *Env, outcome string) {
	now := env.now()
	m := rs.mark
	e := sessionend.Entry{
		At: now.UTC().Format(time.RFC3339), Container: rs.target.Name, ContainerID: rs.target.ID,
		Project: rs.target.Project, Instance: rs.target.Instance, Worktree: m.Worktree, CwdRoot: m.CwdRoot,
		ConfigDir: m.ConfigDir, Sessions: rs.sessionsForLog(), Resume: []string{},
		Reason: sessionend.ReasonRestart, Outcome: outcome,
		ConversationSource: rs.source, TurnState: rs.turnState,
	}
	if rs.newPlan != nil {
		e.NewContainer, e.NewInstance = rs.newPlan.ContainerName, rs.newPlan.Instance
	}
	if rs.source != "record" {
		e.Picker = rs.pickerCmd()
	}
	for _, s := range e.Sessions {
		e.Resume = append(e.Resume, s.Resume)
	}
	if err := sessionend.AppendLog(env.stateDir(), e); err != nil {
		fmt.Fprintf(env.Err, "Warning: could not log the restart to %s: %v\n", hostdirs.SessionEndsLog(env.stateDir()), err)
	}
	n := sessionend.Notice{V: 1, Project: rs.target.Project, At: now.UnixMilli()}
	if outcome != sessionend.OutcomeRestarted {
		n.Entries = append(n.Entries, sessionend.NoticeEntry{Instance: rs.target.Instance, Name: m.Name, SessionID: m.Conversation,
			Resume: rs.resumeCmd(), Reason: sessionend.ReasonRestart})
	}
	for _, j := range rs.joins {
		n.Entries = append(n.Entries, sessionend.NoticeEntry{Instance: rs.target.Instance, Name: j.Name, SessionID: j.SessionID,
			Resume: rs.joinResume(j), Reason: sessionend.ReasonRestartJoin})
	}
	if len(n.Entries) > 0 {
		sessionend.WriteNotice(env.stateDir(), n)
	}
}

// failAfterStop is every failure once the old container was stopped
// (CS-LNCH-220): the launch's own error first, then the way back — the
// conversation's resume command — then the log line and the notice.
func (rs *restartState) failAfterStop(env *Env, err error, outcome string) error {
	rs.stopSignals()
	code := 2
	var ce *execx.CodeError
	switch {
	case errorAs(err, &ce):
		code = ce.Code
		if ce.Msg != "" && ce.Msg != fmt.Sprintf("exit %d", ce.Code) {
			fmt.Fprintln(env.Err, ce.Msg)
		}
	case err != nil:
		fmt.Fprintf(env.Err, "Error: %v\n", err)
	}
	var gr *guardRefusal
	if errorAs(err, &gr) {
		if gr.held {
			fmt.Fprintf(env.Err, "%s was stopped; its conversation is now open in the session named above.\n", rs.who())
		} else {
			fmt.Fprintf(env.Err, "%s was stopped; whether its conversation is open elsewhere now cannot be told (see above).\n", rs.who())
		}
	}
	fmt.Fprintf(env.Err, "The old container of %s was stopped, and its conversation (%s) was not resumed.\n", rs.who(), rs.convLabel())
	fmt.Fprintf(env.Err, "Resume it by hand: %s\n", rs.resumeCmd())
	rs.logOutcome(env, outcome)
	return &execx.CodeError{Code: code}
}

// onChild runs right before the new session child (sessionOpts.onChild):
// the launcher's own handlers take over the signals, the restart is logged,
// and the watcher that tells an early end starts (CS-LNCH-220).
func (rs *restartState) onChild(env *Env, plan *launch.Plan) func() {
	return func() {
		rs.stopSignals()
		rs.newPlan = plan
		rs.logOutcome(env, sessionend.OutcomeRestarted)
		class, err := strconv.Atoi(plan.PIDClass)
		if err != nil {
			class = -1
		}
		rs.watcher = tmuxpane.StartWatcher(tmuxpane.ReadyOptions{
			RegistryDir: plan.RegistryDir, Class: class, Since: time.Now(), ID: rs.mark.Conversation,
		})
	}
}

// afterSession reads how the new session ended (CS-LNCH-220): one that ended
// with a crash code before its claude resumed the conversation, within
// EarlyEnd, gets the way back printed and logged.
func (rs *restartState) afterSession(env *Env, err error) error {
	if err == nil {
		// Exit 0: the session's own end, whenever it came.
		rs.watcher.ChildReturned()
		return nil
	}
	code := 2
	var ce *execx.CodeError
	what := "could not be started"
	if errorAs(err, &ce) {
		code = ce.Code
		if rs.watcher != nil && !rs.watcher.EndedEarly() {
			return err // it was up: a later end is the session's own
		}
		// 78 included: pidslot refused to start claude (CS-GCFG-038).
		what = fmt.Sprintf("ended before it was up (exit %d)", code)
		if ce.Msg != "" && ce.Msg != fmt.Sprintf("exit %d", ce.Code) {
			fmt.Fprintln(env.Err, ce.Msg)
		}
	} else {
		// docker could not be run at all: the session never started.
		rs.watcher.ChildReturned()
		fmt.Fprintf(env.Err, "Error: %v\n", err)
	}
	cfg := rs.mark.ConfigDir
	if cfg == "" {
		cfg = "its config dir"
	}
	fmt.Fprintf(env.Err, "Error: the restarted session of %s (%s) %s.\n", rs.who(), rs.convLabel(), what)
	fmt.Fprintf(env.Err, "       The old container was stopped; the conversation is in %s.\n", cfg)
	fmt.Fprintf(env.Err, "       Resume it by hand: %s\n", rs.resumeCmd())
	rs.logOutcome(env, sessionend.OutcomeStartFailed)
	return &execx.CodeError{Code: code}
}

// signalsForRestart is Env.restartSignals with the real handlers as its
// default: SIGINT, SIGTERM and SIGHUP are caught, never ignored (CS-LNCH-220).
func (e *Env) signalsForRestart() (<-chan os.Signal, func()) {
	if e.restartSignals != nil {
		return e.restartSignals()
	}
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return ch, func() { signal.Stop(ch) }
}

func (rs *restartState) stopSignals() {
	if rs.stopSig != nil {
		rs.stopSig()
		rs.stopSig = nil
	}
}

// sleep waits d before the stop; false when a signal came (it aborts).
func (rs *restartState) sleep(d time.Duration) bool {
	if rs.caughtNow() {
		return false
	}
	select {
	case s := <-rs.sig:
		rs.caught = s
		return false
	case <-time.After(d):
		return true
	}
}

// waitAfterStop waits d after the stop began: a signal is recorded and the
// wait goes on, since docker already has the stop (CS-LNCH-220).
func (rs *restartState) waitAfterStop(d time.Duration) {
	t := time.After(d)
	for {
		select {
		case s := <-rs.sig:
			if rs.caught == nil {
				rs.caught = s
			}
		case <-t:
			return
		}
	}
}

// caughtNow reports, without waiting, whether a signal came.
func (rs *restartState) caughtNow() bool {
	if rs.caught != nil {
		return true
	}
	if rs.sig == nil {
		return false
	}
	select {
	case s := <-rs.sig:
		rs.caught = s
		return true
	default:
		return false
	}
}

// signalNumber is a signal's number, 2 (SIGINT) when unknown.
func signalNumber(s os.Signal) int {
	if n, ok := s.(syscall.Signal); ok {
		return int(n)
	}
	return int(syscall.SIGINT)
}
