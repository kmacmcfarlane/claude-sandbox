package launch

import (
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// Terminal identity (CS-LNCH-178..181, CS-SESS-090..092).
//
// Claude Code keys some terminal handling on environment variables it reads
// once at start. The one that matters here is TERMINAL_EMULATOR: with
// "JetBrains-JediTerm" (GoLand's and every JetBrains IDE's terminal) Claude
// Code drops the plain Up/Down arrows JediTerm sends along with its mouse
// wheel reports; without it those arrows reach the prompt, so a wheel scroll
// in the fullscreen TUI also moves the input field (cursor, history recall).
// docker create passes none of the launcher's environment, so the launcher
// passes this identity explicitly on every launch that creates a TTY session
// for a person, and on every join.
//
// TerminalEnv is a list so a later decision can widen it by name; every name
// in it must carry a non-secret value, since the value rides argv and the
// claude-sandbox.terminal label. Deliberately NOT in it:
//   - TERM: docker -t sets xterm; a host TERM such as tmux-256color or
//     xterm-kitty needs terminfo the image may lack.
//   - TMUX, TMUX_PANE: host socket paths; inside the container they would
//     send claude after a tmux it cannot reach, and TMUX set changes its
//     synchronized-output decision.
//   - TERM_PROGRAM, COLORTERM and kin: inside tmux they are tmux's own, and
//     forwarding them without TMUX makes a mixed state nobody has tested;
//     COLORTERM changes colour depth. Widened only on evidence.
//   - any prefix wildcard.
var TerminalEnv = []string{TerminalEmulatorVar}

// TerminalEmulatorVar is the variable JetBrains terminals set and Claude Code
// reads for its JediTerm handling.
const TerminalEmulatorVar = "TERMINAL_EMULATOR"

// LabelTerminal records the terminal identity the container's claude sees
// (CS-LNCH-181): TerminalNone, or "NAME=value" pairs joined by ";". Absent or
// empty on a container that predates it. Never hashed.
const LabelTerminal = "claude-sandbox.terminal"

// TerminalNone is LabelTerminal's value when no identity variable is set in
// the container, the same sentinel convention as PeerRootNone.
const TerminalNone = "none"

// terminalValueMax caps a cleaned identity value, in runes.
const terminalValueMax = 64

// TerminalSource yields the source value S of an identity variable: the
// launcher's own environment today. It is a seam: a later source (tmux's
// session environment, OQ-B) changes only S, never the rules below.
type TerminalSource func(name string) (string, bool)

// TerminalVar is one TerminalEnv name, resolved.
type TerminalVar struct {
	Name string
	// Source is S, and SourceSet whether it counts as set (empty = unset).
	Source    string
	SourceSet bool
	// File, FileSet, FileSrc and FilePath are the env-file walk
	// (cascade.EnvFilesValue): docker's own result over the files.
	File     string
	FileSet  bool
	FileSrc  cascade.EnvSource
	FilePath string
}

// pinned reports rule 2: the walk's last effective line assigns the value,
// an explicit file choice the launcher leaves in force.
func (v TerminalVar) pinned() bool { return v.FileSrc == cascade.EnvSourceAssign }

// TerminalResult is ResolveTerminal's answer for every TerminalEnv name.
type TerminalResult struct {
	Vars []TerminalVar
}

// ResolveTerminal resolves every TerminalEnv name for one launch, join or
// attach: src gives S, snap is the cascade env files snapshotted once, and
// clientEnv is the docker client's environment (the launcher's), against
// which a bare env-file line resolves. One resolver for create (env and
// label), join (set or unset) and the attach comparison, so the four cannot
// disagree about which terminal they mean.
func ResolveTerminal(src TerminalSource, snap []cascade.EnvFile, clientEnv func(string) (string, bool)) TerminalResult {
	var r TerminalResult
	for _, name := range TerminalEnv {
		v := TerminalVar{Name: name}
		if src != nil {
			if s, ok := src(name); ok && s != "" {
				v.Source, v.SourceSet = s, true
			}
		}
		v.File, v.FileSet, v.FileSrc, v.FilePath = cascade.EnvFilesValue(snap, name, cascade.LookupEnv(clientEnv))
		r.Vars = append(r.Vars, v)
	}
	return r
}

// CreateEnv is the -e specs a new container gets (CS-LNCH-178/179). passive
// marks headless and ralph, which pass none (CS-LNCH-180). Rule 2 (the walk
// ends on an assignment): none, docker's result over the files stands. Rule
// 3: NAME=<S> when S is set; NAME= when S is unset but a bare line would pass
// the docker client's value (possible only once S comes from elsewhere than
// the launcher's environment); else none.
func (r TerminalResult) CreateEnv(passive bool) []string {
	if passive {
		return nil
	}
	var out []string
	for _, v := range r.Vars {
		switch {
		case v.pinned():
		case v.SourceSet:
			out = append(out, v.Name+"="+v.Source)
		case v.FileSrc == cascade.EnvSourceBare:
			out = append(out, v.Name+"=")
		}
	}
	return out
}

// JoinEnv is what a join's docker exec does per name (CS-SESS-090/092): set
// holds the -e specs, unset the names to remove with "/usr/bin/env -u" in
// front of the command. Every name is either set or unset, never left to the
// container: the container may hold the creator's value (a bare env-file
// line resolved against the creator's environment). Rule 2 sets the walk's
// value (unset when empty); rule 3 sets S (unset when unset).
func (r TerminalResult) JoinEnv() (set, unset []string) {
	for _, v := range r.Vars {
		val, ok := v.Source, v.SourceSet
		if v.pinned() {
			val, ok = v.File, v.FileSet
		}
		if ok {
			set = append(set, v.Name+"="+val)
		} else {
			unset = append(unset, v.Name)
		}
	}
	return set, unset
}

// Label is the claude-sandbox.terminal value (CS-LNCH-181): what the
// container's claude sees, cleaned. passive (headless, ralph) takes the walk
// alone, since those pass no -e; otherwise rule 2 takes the walk and rule 3
// takes S. TerminalNone when nothing is set.
func (r TerminalResult) Label(passive bool) string {
	var parts []string
	for _, v := range r.Vars {
		val, ok := v.Source, v.SourceSet
		if passive || v.pinned() {
			val, ok = v.File, v.FileSet
		}
		if !ok {
			continue
		}
		if c := cleanTerminalValue(val); c != "" {
			parts = append(parts, v.Name+"="+c)
		}
	}
	if len(parts) == 0 {
		return TerminalNone
	}
	return strings.Join(parts, ";")
}

// PinnedBy names the env file whose assignment decides the identity, ""
// when no name is pinned by a file (the attach note's remedy, CS-SESS-091).
func (r TerminalResult) PinnedBy() string {
	for _, v := range r.Vars {
		if v.pinned() {
			return v.FilePath
		}
	}
	return ""
}

// cleanTerminalValue makes a value safe for a label and a message: printable
// (registry.Printable), without the label's own separators "=" and ";" or the
// ps field separator, capped at terminalValueMax runes.
func cleanTerminalValue(s string) string {
	s = registry.Printable(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '=', ';', '\x1f':
			return -1
		}
		return r
	}, s)
	if rs := []rune(s); len(rs) > terminalValueMax {
		s = string(rs[:terminalValueMax])
	}
	return strings.TrimSpace(s)
}

// DescribeTerminal renders a label value for a message: "TERMINAL_EMULATOR
// unset" for TerminalNone, else the value as recorded.
func DescribeTerminal(label string) string {
	if label == TerminalNone {
		return strings.Join(TerminalEnv, ", ") + " unset"
	}
	return label
}
