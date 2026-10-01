package main

// The session child (CS-LNCH-085..098, CS-SESS-059/060). Every interactive
// path — a new container's "docker start -ai", "docker attach", a join's
// "docker exec" and a headless launch — runs docker as a child the launcher
// waits on, instead of exec'ing into it, so the launcher is still there when
// the session ends and can say why: an OOM kill is otherwise
// indistinguishable from Claude Code dying.

import (
	"fmt"
	"io"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// sessionKind selects how a finished session child is judged.
type sessionKind int

const (
	// primarySession: the child's return means the container died or the
	// client detached; the die event tells which (attach).
	primarySession sessionKind = iota
	// reservedSession: a primary session whose container this launch
	// created and "docker start" started — or failed to (CS-LNCH-096).
	reservedSession
	// joinedSession: a "docker exec"; the container normally outlives it.
	joinedSession
)

// sessionOpts describes one session child.
type sessionOpts struct {
	kind sessionKind
	// fallback is the memory limit the caller knows, used when the events
	// carry no limit label.
	fallback oomreport.Limit
	// headless suppresses the terminal reset: its stdio is an SDK client's
	// pipe (CS-LNCH-092).
	headless bool
	// after runs once the session ended and its reports are printed, while
	// the signal handlers are still installed, so a signal during it still
	// exits with the child's status (CS-GCFG-001, CS-LNCH-097). Not run
	// after a forwarded or late signal, or a start that never ran.
	after func()
	// mark is the tmux pane mark set while the child runs (CS-TMUX-010);
	// nil for none. Ignored when headless or outside tmux (CS-TMUX-014).
	mark *paneMark
	// onChild runs right after the pane is marked and right before the
	// session child starts: a restore releases its start lock there after
	// an attach, or starts its readiness watcher (CS-TMUX-060/061). nil on
	// every other path.
	onChild func()
}

// sessionEnd is how a session child ended.
type sessionEnd struct {
	// code is the child's status, passed through as the launcher's own.
	code int
	// gone is true when the container's die event was seen: nothing mounts
	// the launch's shadow directory any more (CS-LNCH-094).
	gone bool
	// neverStarted is true when a reserved container is still "created"
	// after its "docker start" returned (CS-LNCH-096).
	neverStarted bool
}

// isTerminal is Env.IsTerminal with the real check as its default.
func (e *Env) isTerminal(w io.Writer) bool {
	if e.IsTerminal != nil {
		return e.IsTerminal(w)
	}
	return execx.IsTerminal(w)
}

// runSession runs c for container and reports an OOM kill when the session
// ends (CS-LNCH-087..092). The child's signal handlers stay installed until
// the report is written (CS-LNCH-097).
func runSession(env *Env, c execx.Cmd, container string, o sessionOpts) (end sessionEnd, err error) {
	// Subscribed before the child starts, from a moment before it, so no
	// event of this session can be missed (CS-LNCH-087).
	w := oomreport.Start(env.Runner, container, env.now())
	defer w.Stop()

	// CS-TMUX-010: the pane mark is set before the child starts and settled
	// once it returned: unset on a clean exit, a crash or a detach
	// (CS-TMUX-015), pending after a stop from outside (CS-TMUX-071), and the
	// prior mark back when the session never really started (CS-TMUX-018/019).
	pane := beginMark(env, o)
	putBack := false
	pending := false
	if o.onChild != nil {
		o.onChild()
	}

	started := env.now()
	res, err := env.Runner.RunSession(c)
	defer res.Done()
	// Deferred after res.Done, so it runs first: while the session's signal
	// handlers are still installed, and a signal cannot cut it short. The
	// rules apply in order (CS-TMUX-062): a start that never ran, then a
	// restore resume that ended before it was resumed, then CS-TMUX-071.
	defer func() {
		switch {
		case putBack || err != nil || end.neverStarted:
			pane.end(markPutBack)
		case o.mark != nil && o.mark.keepUnlessReady != nil && o.mark.keepUnlessReady():
			// The prior is the restore's pending row; the current mark of a
			// session under a minute old has no conversation yet.
			pane.end(markPutBack)
		case pending:
			pane.end(markPending)
		default:
			pane.end(markUnset)
		}
	}()
	if err != nil {
		return sessionEnd{code: res.Code}, err
	}
	end = sessionEnd{code: res.Code}
	if pane != nil && vanished(env, o, res.Code, started) {
		// The prior mark goes back before any CS-TMUX-071 row: no probe.
		putBack = true
		pane.decided = true
	}
	if res.Forwarded != nil {
		// CS-LNCH-091: whoever sent the signal ended the session on
		// purpose, and an SDK client expects a prompt exit: no die wait, no
		// report. The shadow directory is left to a later launch's sweep.
		// A marked pane still looks for a stop from outside first: systemd
		// signals the launchers of a shutting-down host (CS-TMUX-071).
		pending = pane.stoppedFromOutside(env, w.Snapshot())
		return end, nil
	}

	if o.kind == reservedSession {
		// CS-LNCH-096: a start that never ran leaves a "created" container:
		// no die will ever come, so there is nothing to wait for.
		if state, _ := sessions.Inspect(env.Runner, container); state == sessions.StateCreated {
			end.neverStarted = true
			return end, nil
		}
	}

	var out oomreport.Outcome
	var verdict oomreport.Verdict
	var stopped bool
	switch o.kind {
	case primarySession, reservedSession:
		out, stopped = w.AwaitDeath(res.Late)
		verdict = oomreport.Primary(out)
		end.gone = out.Died
	case joinedSession:
		// A marked join waits for its container's die (CS-TMUX-071).
		out, stopped = w.AwaitJoined(res.Code, pane != nil, res.Late)
		verdict = oomreport.Joined(res.Code, out)
	}
	if stopped {
		// CS-LNCH-097: a signal after the child exited asks for the exit
		// now — with the child's status, silently. Like a forwarded signal,
		// it unsets unless a stop from outside is in evidence.
		pending = pane.stoppedFromOutside(env, out)
		return end, nil
	}
	if end.code == globalcfg.ExitLink {
		// CS-GCFG-038/039: pidslot refused to start claude without the
		// global-config link. Inferred from the status alone, hence "likely".
		fmt.Fprint(env.Err, globalcfg.ExitMessage(container))
	}
	lim := out.Limit
	if lim.Value == "" {
		lim = o.fallback
	}
	switch verdict {
	case oomreport.Killed:
		if !o.headless && env.isTerminal(env.Err) {
			// The TUI died with its modes set; the docker client restored
			// only the termios (CS-LNCH-092).
			io.WriteString(env.Err, oomreport.TerminalReset+"\n")
		}
		fmt.Fprint(env.Err, oomreport.KilledReport(out.OOMKills, lim))
	case oomreport.Survived:
		fmt.Fprint(env.Err, oomreport.SurvivedReport(out.OOMKills, lim))
	}
	// CS-TMUX-071: after the report, so a probe or an inspect never delays it.
	pending = pane.pendingAfter(env, o, w, res.Code, out, container)
	if o.after != nil {
		// Still under the session's handlers (deferred res.Done): a signal
		// now lands on Late and is dropped, and the child's status stands.
		o.after()
	}
	return end, nil
}

// sessionExit turns a session's status into the launcher's own exit
// (CS-LNCH-085): the code, silently.
func sessionExit(code int) error {
	if code == 0 {
		return nil
	}
	return &execx.CodeError{Code: code, Msg: fmt.Sprintf("exit %d", code)}
}
