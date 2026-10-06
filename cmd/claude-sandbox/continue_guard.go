package main

// The continue guard's wiring (CS-LNCH-182, CS-SESS-093..096; plan
// continue-resume-guard). "--continue" names no conversation: claude picks the
// newest one in its directory inside the container, and Claude Code (2.1.290)
// skips only conversations held by live background sessions. The launcher
// refuses (exit 4) while any live session has a conversation open in the
// directories claude would continue in: an advisory check before the image
// work (records only), the authoritative one under the launch lock before
// docker create, and one before a join's docker exec. The decision itself is
// resumeguard.RunContinue.

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// continueGuarded reports whether a launch is a guarded continue (CS-LNCH-182):
// its passthrough continues without certainly forking (the walker,
// CS-LNCH-183). Headless counts (operator answer 85 a: the Agent SDK's
// "continue: true" becomes --continue); ralph's claude takes no passthrough.
func continueGuarded(passthrough []string, ralph bool) bool {
	return !ralph && tmuxpane.GuardedContinue(passthrough)
}

// continuePath selects the refusal's last lines (CS-SESS-093): only a terminal
// can use claude's picker.
type continuePath int

const (
	continueTTY      continuePath = iota // --new, the tier-1 [n], a join
	continueDetached                     // --detach: nobody is attached
	continueHeadless                     // an SDK client reads the log
)

func pathOf(headless, detached bool) continuePath {
	switch {
	case headless:
		return continueHeadless
	case detached:
		return continueDetached
	}
	return continueTTY
}

// continueTarget is where claude would continue (CS-SESS-093): dir exactly
// (the container workdir, or a join's exec -w), and, inside a git work tree,
// every named worktree — the launch's own (worktree, final under the lock for
// a new container) and every -w/--worktree name in the passthrough, the
// walker's collected list rather than its single value, so a flag-table drift
// cannot hide one (CS-LNCH-183) — with everything below it.
func continueTarget(dir, project, gitRoot, worktree string, passthrough []string) resumeguard.ContinueTarget {
	t := resumeguard.ContinueTarget{Exact: []string{dir}, Project: project}
	if gitRoot == "" {
		return t // claude refuses --worktree outside a git repository
	}
	var names []string
	if worktree != "" {
		names = append(names, worktree)
	}
	for _, n := range tmuxpane.WalkClaudeArgs(passthrough).Worktrees {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	for _, n := range names {
		t.Under = append(t.Under, filepath.Join(gitRoot, launch.WorktreeDir, n))
	}
	return t
}

// continueCheck is the guard's Check for env's seams.
func continueCheck(env *Env, found []sessions.Session, derr error, home, configDir string, t resumeguard.ContinueTarget) resumeguard.Check {
	return resumeguard.Check{
		Sessions: found, DiscoveryErr: derr, Runner: env.Runner,
		Home: home, ConfigDir: configDir, ProcRoot: env.ProcRoot, MachineIDPath: env.MachineIDPath,
		Continue: &t,
	}
}

// guardContinue is the authoritative check for a new container, under the
// launch lock after discovery and before docker create (CS-SESS-094).
func guardContinue(env *Env, in launch.Inputs, gitRoot string, found []sessions.Session, derr error) error {
	t := continueTarget(in.ProjectDir, in.ProjectDir, gitRoot, in.Worktree, in.Passthrough)
	v := continueCheck(env, found, derr, in.Home, in.ConfigDir(), t).RunContinue()
	if !v.Open {
		return nil
	}
	return continueRefusal(v, in.ProjectDir, in.Home, pathOf(in.Headless, in.Detached))
}

// preBuildContinue is the advisory check before any image work (CS-SESS-094):
// one discovery, rules b' and h' only, and a refusal only on a live record — a
// label, a reservation or a "cannot tell" is left to the check under the lock,
// so a transient docker failure or an orphaned reservation never costs the
// early exit.
func preBuildContinue(env *Env, projectDir, gitRoot, worktree string, passthrough []string, headless, detached bool) error {
	found, err := sessions.DiscoverAllUncounted(env.Runner)
	if err != nil {
		return nil
	}
	_, _, _, home := hostIdentity(env.Getenv)
	t := continueTarget(projectDir, projectDir, gitRoot, worktree, passthrough)
	c := continueCheck(env, found, nil, home, configDirOf(env, home), t)
	c.RecordsOnly = true
	v := c.RunContinue()
	if v.Holder == nil && v.HostPID == 0 {
		return nil
	}
	return continueRefusal(v, projectDir, home, pathOf(headless, detached))
}

// guardJoinContinue runs before a join's docker exec whose passthrough is a
// guarded continue (CS-SESS-095, operator answer 86 a): T is the exec's -w dir
// plus the join's --worktree=NAME and the passthrough's named worktrees. Joins
// take no launch lock and remove nothing, so an orphaned reservation
// (CS-SESS-052's age) is left out of rule a' instead.
func guardJoinContinue(env *Env, projectDir string, wt worktreeChoice, passthrough []string) error {
	found, derr := sessions.DiscoverAllUncounted(env.Runner)
	if derr == nil {
		stale := sessions.Stale(found, env.now(), staleReservationAge)
		found = slices.DeleteFunc(found, func(s sessions.Session) bool {
			return slices.ContainsFunc(stale, func(o sessions.Session) bool { return o.Name == s.Name })
		})
	}
	named := ""
	if wt.Enabled {
		named = wt.Name // a bare --worktree is a fresh worktree: nothing to add
	}
	_, _, _, home := hostIdentity(env.Getenv)
	t := continueTarget(projectDir, projectDir, wt.Root, named, passthrough)
	v := continueCheck(env, found, derr, home, configDirOf(env, home), t).RunContinue()
	if !v.Open {
		return nil
	}
	return continueRefusal(v, projectDir, home, continueTTY)
}

// configDirOf is the launcher's resolved Claude config dir, as launch.Inputs
// resolves it.
func configDirOf(env *Env, home string) string {
	in := launch.Inputs{Home: home, Getenv: env.Getenv}
	return in.ConfigDir()
}

// continueRefusal is the exit-4 message (CS-SESS-093/094). dir is the
// directory the launch would continue in, named when the verdict has no
// holder record's cwd.
func continueRefusal(v resumeguard.Verdict, dir, home string, path continuePath) error {
	cwd := dir
	if v.Cwd != "" {
		cwd = v.Cwd
	}
	id := v.SessionID
	const why = "       a second session on one conversation would interleave writes into one transcript.\n"

	var msg string
	attach := ""
	switch {
	case v.Holder != nil:
		h := v.Holder
		who := h.Name
		if h.Instance != "" {
			who = "'" + h.Instance + "' (" + h.Name + ")"
		}
		if v.Starting {
			msg = fmt.Sprintf("Error: --continue would reopen the newest conversation in %s,\n"+
				"       and %s is starting a session there that opens a conversation;\n", cwd, who) + why +
				"       Retry once it is up.\n"
		} else {
			msg = fmt.Sprintf("Error: --continue would reopen the newest conversation in %s,\n"+
				"       and %s has conversation %s open there;\n", cwd, who, id) + why
		}
		if h.Instance != "" && h.Mode != sessions.ModeRalph && h.Mode != sessions.ModeHeadless {
			attach = "       Attach to it:  " + attachCommand(h.Project, home, h.Instance) + "\n"
		}
	case v.HostPID != 0:
		msg = fmt.Sprintf("Error: --continue would reopen the newest conversation in %s,\n"+
			"       and a claude process on this host (pid %d) has conversation %s open there;\n", cwd, v.HostPID, id) + why
	default:
		msg = fmt.Sprintf("Error: cannot tell whether a conversation in %s is already open elsewhere\n"+
			"       (%s), so --continue is not run.\n"+
			"       Fix that and retry.\n", cwd, v.Reason)
	}

	open := ""
	switch path {
	case continueHeadless:
		if id != "" {
			open = " (" + id + " is the one already open)"
		}
		msg += "       Or fork it:    pass --fork-session\n" +
			"       Or name one:   pass --resume <conversation-id> for the conversation you want" + open
	case continueDetached:
		if id != "" {
			open = id + " is the one already open; "
		}
		msg += attach +
			"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
			"       Or name one:   pass --resume=<conversation-id> after -- for the conversation you want (" +
			open + "a picker would wait in a session nobody is attached to)"
	default:
		if id != "" {
			open = "; " + id + " is the one already open"
		}
		msg += attach +
			"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
			"       Or pick one:   use --resume instead of --continue (claude's picker" + open + ")"
	}
	return exitErr(exitResumeLive, "%s", msg)
}

// continueNote is the tier-1 prompt's note for a guarded continue
// (CS-SESS-096).
const continueNote = "Note: --continue reopens the newest conversation in this directory, which a session above may have open; [n] and [j] are refused while one does — [b] forks it instead."
