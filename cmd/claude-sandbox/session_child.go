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
func runSession(env *Env, c execx.Cmd, container string, o sessionOpts) (sessionEnd, error) {
	// Subscribed before the child starts, from a moment before it, so no
	// event of this session can be missed (CS-LNCH-087).
	w := oomreport.Start(env.Runner, container, env.now())
	defer w.Stop()

	res, err := env.Runner.RunSession(c)
	defer res.Done()
	if err != nil {
		return sessionEnd{code: res.Code}, err
	}
	end := sessionEnd{code: res.Code}
	if res.Forwarded != nil {
		// CS-LNCH-091: whoever sent the signal ended the session on
		// purpose, and an SDK client expects a prompt exit: no die wait, no
		// report. The shadow directory is left to a later launch's sweep.
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
		out, stopped = w.AwaitJoined(res.Code, res.Late)
		verdict = oomreport.Joined(res.Code, out)
	}
	if stopped {
		// CS-LNCH-097: a signal after the child exited asks for the exit
		// now — with the child's status, silently.
		return end, nil
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
