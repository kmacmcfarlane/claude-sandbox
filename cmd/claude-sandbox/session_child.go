package main

// The session child (CS-LNCH-085..094, CS-SESS-059/060). Every interactive
// path — a new container's "docker start -ai", "docker attach", a join's
// "docker exec" and a headless launch — runs docker as a child the launcher
// waits on, instead of exec'ing into it, so the launcher is still there when
// the session ends and can say why: the container's OOM killer is otherwise
// indistinguishable from Claude Code dying.

import (
	"fmt"
	"io"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/oomreport"
)

// sessionKind selects how a finished session child is judged.
type sessionKind int

const (
	// primarySession: the child's return means the container died or the
	// client detached; the die event tells which (new, attach, headless).
	primarySession sessionKind = iota
	// joinedSession: a "docker exec"; the container normally outlives it.
	joinedSession
)

// sessionEnd is how a session child ended.
type sessionEnd struct {
	// code is the child's status, passed through as the launcher's own.
	code int
	// gone is true when the container's die event was seen: nothing mounts
	// the launch's shadow directory any more (CS-LNCH-094).
	gone bool
}

// runSession runs c for container and reports an OOM kill when the session
// ends (CS-LNCH-087..092). fallback is the memory limit the caller knows,
// used when the events carry no limit label. headless suppresses the
// terminal reset: its stdio is an SDK client's pipe (CS-LNCH-092).
func runSession(env *Env, c execx.Cmd, container string, kind sessionKind, fallback oomreport.Limit, headless bool) (sessionEnd, error) {
	// Subscribed before the child starts, from a moment before it, so no
	// event of this session can be missed (CS-LNCH-087).
	w := oomreport.Start(env.Runner, container, env.now())
	defer w.Stop()

	res, err := env.Runner.RunSession(c)
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

	var o oomreport.Outcome
	var verdict oomreport.Verdict
	switch kind {
	case primarySession:
		o = w.Await(oomreport.DieWait, oomreport.Died)
		verdict = oomreport.Primary(o)
		end.gone = o.Died
	case joinedSession:
		if res.Code == oomreport.OOMExit {
			o = w.Await(oomreport.DieWait, oomreport.SawOOM)
		} else {
			o = w.Await(oomreport.JoinGrace, oomreport.Never)
		}
		verdict = oomreport.Joined(res.Code, o)
	}
	lim := o.Limit
	if lim.Value == "" {
		lim = fallback
	}
	switch verdict {
	case oomreport.Killed:
		if !headless && execx.IsTerminal(env.Err) {
			// The TUI died with its modes set; the docker client restored
			// only the termios (CS-LNCH-092).
			io.WriteString(env.Err, oomreport.TerminalReset+"\n")
		}
		fmt.Fprint(env.Err, oomreport.KilledReport(o.OOMKills, lim))
	case oomreport.Survived:
		fmt.Fprint(env.Err, oomreport.SurvivedReport(o.OOMKills, lim))
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
