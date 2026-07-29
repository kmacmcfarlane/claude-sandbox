// Package prompt is the seam for interactive terminal prompts. Every prompt
// in claude-sandbox flows through a Prompter so flags (--yes, per-prompt
// pairs) and tests can script answers, and no-tty runs never block.
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Prompter answers yes/no questions.
type Prompter interface {
	// Confirm prints preamble (if any) and question, and returns the answer.
	// def is returned on Enter, timeout, EOF, or when no terminal is attached.
	Confirm(preamble, question string, def bool, timeout time.Duration) bool
	// Ask prints preamble/question and returns the raw trimmed answer.
	// Returns "" on Enter, timeout, EOF, or no terminal — callers treat ""
	// as "accept the default" and can distinguish it from an explicit answer.
	Ask(preamble, question string, timeout time.Duration) string
	// Interactive reports whether a terminal is attached (prompts can be shown).
	Interactive() bool
}

// TTY prompts on the controlling terminal (/dev/tty), mirroring the bash
// scripts: prompts work even when stdin/stdout are pipes.
type TTY struct {
	Out io.Writer // messages; defaults to os.Stderr
}

func (t *TTY) out() io.Writer {
	if t.Out != nil {
		return t.Out
	}
	return os.Stderr
}

func (t *TTY) Interactive() bool {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (t *TTY) Confirm(preamble, question string, def bool, timeout time.Duration) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	return Parse(t.Ask(preamble, question+" "+hint, timeout), def)
}

func (t *TTY) Ask(preamble, question string, timeout time.Duration) string {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return ""
	}
	defer tty.Close()
	if preamble != "" {
		fmt.Fprintln(t.out(), preamble)
	}
	fmt.Fprintf(t.out(), "%s ", question)

	lines := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(tty)
		if s.Scan() {
			lines <- s.Text()
		} else {
			lines <- ""
		}
	}()
	var ans string
	if timeout > 0 {
		select {
		case ans = <-lines:
		case <-time.After(timeout):
			fmt.Fprintln(t.out())
			return ""
		}
	} else {
		ans = <-lines
	}
	return strings.TrimSpace(ans)
}

// Parse interprets a free-form answer; empty/unrecognized input yields def.
func Parse(ans string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes", "true":
		return true
	case "n", "no", "false":
		return false
	default:
		return def
	}
}

// Fixed is a non-interactive Prompter that always answers with the default
// (used for --yes and no-tty automation).
type Fixed struct {
	// TreatInteractive reports prompts as available even though answers are
	// canned (--yes on a tty).
	TreatInteractive bool
	Out              io.Writer
}

func (f *Fixed) Interactive() bool { return f.TreatInteractive }

func (f *Fixed) Confirm(preamble, question string, def bool, _ time.Duration) bool {
	if f.Out != nil {
		if preamble != "" {
			fmt.Fprintln(f.Out, preamble)
		}
		fmt.Fprintf(f.Out, "%s -> %v (assumed)\n", question, def)
	}
	return def
}

func (f *Fixed) Ask(preamble, question string, _ time.Duration) string {
	if f.Out != nil {
		if preamble != "" {
			fmt.Fprintln(f.Out, preamble)
		}
		fmt.Fprintf(f.Out, "%s -> (default assumed)\n", question)
	}
	return ""
}

// Scripted answers prompts from a queue, for tests. Missing answers fall back
// to the default.
type Scripted struct {
	Answers []string
	Asked   []string
	IsTTY   bool
	Out     io.Writer
}

func (s *Scripted) Interactive() bool { return s.IsTTY }

func (s *Scripted) Confirm(preamble, question string, def bool, d time.Duration) bool {
	return Parse(s.Ask(preamble, question, d), def)
}

func (s *Scripted) Ask(preamble, question string, _ time.Duration) string {
	s.Asked = append(s.Asked, question)
	if s.Out != nil && preamble != "" {
		fmt.Fprintln(s.Out, preamble)
	}
	if len(s.Answers) == 0 {
		return ""
	}
	ans := s.Answers[0]
	s.Answers = s.Answers[1:]
	return strings.TrimSpace(ans)
}
