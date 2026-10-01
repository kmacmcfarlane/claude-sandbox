package tmuxpane

import (
	"regexp"
	"slices"
	"strings"

	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
)

// What a restore replays, and what it only names (CS-TMUX-013, operator
// decision 49 = plan 09 A6 with A7's note). A restore resume replays the
// session's identity — project, raw CLAUDE_CONFIG_DIR, worktree (explicit
// both ways), a --model given on the command line — plus the claude flags in
// ReplayAllowlist: session setup that --resume does not restore and that does
// not widen what the session may do. Every other flag given at launch is
// recorded by NAME only, never by value, so a restore can say in one line
// what it left out. Everything else re-resolves from the cascade, as a hand
// relaunch would. This file is the one place these lists live, so the
// allowlist can grow (the names the note prints are the evidence for it).

// arity is how many tokens a claude flag takes after it (commander grammar).
type arity int

const (
	noValue       arity = iota // a boolean flag
	oneValue                   // <value>: always takes the next token
	optionalValue              // [value]: takes the next token unless it starts with "-"
	variadic                   // <values...>: takes tokens until one starts with "-"
)

// ReplayAllowlist is the claude flags a restore replays with their values
// (plan 09 § A.2 row A6). Widening flags stay out: host access, dangerous
// mode, --permission-mode, --allowedTools, --mcp-config, --settings,
// --plugin-*, --remote-control, --channels. Dropping a RESTRICTING flag
// (--disallowedTools, --bare, --restricted) would widen the resumed session,
// which is why they are here.
var ReplayAllowlist = map[string]arity{
	"--add-dir":                   variadic,
	"--append-system-prompt":      oneValue,
	"--append-system-prompt-file": oneValue,
	"--agent":                     oneValue,
	"--effort":                    oneValue,
	"--disallowedTools":           variadic,
	"--disallowed-tools":          variadic,
	"--tools":                     variadic,
	"--strict-mcp-config":         noValue,
	"--bare":                      noValue,
	"--restricted":                noValue,
	"--safe-mode":                 noValue,
}

// sessionFamily is the claude flags that select or name the conversation.
// A restore rewrites them to "--resume <id> [--name <n>]", so they are never
// replayed and never named.
var sessionFamily = map[string]bool{
	"--resume": true, "-r": true, "--continue": true, "-c": true,
	"--session-id": true, "--from-pr": true, "--teleport": true,
	"--fork-session": true, "--name": true, "-n": true,
}

// claudeArity is the value arity of the claude flags that take one
// (Claude Code 2.1.284 "claude --help"), so the scan never mistakes a value
// for a positional. A flag absent from here and from ReplayAllowlist is a
// boolean when known to take none, else unknown (see ScanPassthrough).
var claudeArity = map[string]arity{
	"--agents": oneValue, "--allowedTools": variadic, "--allowed-tools": variadic,
	"--autocompact": oneValue, "--betas": variadic, "--client-data-url": oneValue,
	"--cloud": optionalValue, "--debug": optionalValue, "-d": optionalValue,
	"--debug-file": oneValue, "--environment": oneValue, "--fallback-model": oneValue,
	"--file": variadic, "--from-pr": optionalValue, "--input-format": oneValue,
	"--json-schema": oneValue, "--max-budget-usd": oneValue, "--max-turns": oneValue,
	"--mcp-config": variadic, "--model": oneValue, "--name": oneValue, "-n": oneValue,
	"--output-format": oneValue, "--permission-mode": oneValue,
	"--permission-prompts": oneValue, "--permission-prompt-tool": oneValue,
	"--plugin-dir": oneValue, "--plugin-url": oneValue,
	"--prompt-suggestions": optionalValue, "--remote-control": optionalValue,
	"--remote-control-session-name-prefix": oneValue,
	"--resume":                             optionalValue, "-r": optionalValue, "--session-id": oneValue,
	"--setting-sources": oneValue, "--settings": oneValue, "--system-prompt": oneValue,
	"--system-prompt-file": oneValue, "--system-prompt-snapshot": oneValue,
	"--teleport": optionalValue, "--worktree": optionalValue, "-w": optionalValue,
	"--channels": variadic,
}

// claudeBoolean is the known claude flags that take no value.
var claudeBoolean = map[string]bool{
	"--allow-dangerously-skip-permissions": true, "--ax-screen-reader": true,
	"--bg": true, "--background": true, "--brief": true, "--chrome": true,
	"--continue": true, "-c": true, "--dangerously-skip-permissions": true,
	"--disable-slash-commands": true, "--exclude-dynamic-system-prompt-sections": true,
	"--fork-session": true, "--forward-subagent-text": true, "--ide": true,
	"--include-hook-events": true, "--include-partial-messages": true,
	"--no-chrome": true, "--no-session-persistence": true, "--print": true, "-p": true,
	"--replay-user-messages": true, "--tmux": true, "--verbose": true,
	"-h": true, "--help": true, "-v": true, "--version": true,
}

// LauncherNamed is the launcher flags a restore never replays but names when
// they were given (canonical spelling): each widens what the session may do,
// and a restore runs unattended (plan 09 § A.3). The remaining launcher flags
// are identity a restore replays itself (--model, --worktree, --no-worktree)
// or restore-owned / one-shot (--new, --branch, --detach, --attach, --join,
// --ralph, --limit, --rebuild, --update, --no-update-check,
// --no-session-check, --allow-config-drift), and are never named.
var LauncherNamed = []string{"--dangerous", "--ssh", "--git", "--docker-socket", "--aws", "--package-caches"}

// EnvNamed is the launcher environment switches named when set non-empty
// (plan 09 § A.1). CLAUDE_SANDBOX_WORKTREE is not: the worktree is replayed
// explicitly both ways. Names only; values are never recorded.
var EnvNamed = []string{
	"CLAUDE_SANDBOX_DANGEROUS", "CLAUDE_SANDBOX_BASE_ONLY",
	"CLAUDE_SANDBOX_DOCKERFILE", "CLAUDE_SANDBOX_DOCKERFILE_DIR",
	"CLAUDE_SANDBOX_SHARED_PEER_REGISTRY", "CLAUDE_SANDBOX_OOM_SCORE_ADJ",
	"CLAUDE_SANDBOX_HOST_ACCESS_SSH_ENABLED", "CLAUDE_SANDBOX_HOST_ACCESS_GIT_ENABLED",
	"CLAUDE_SANDBOX_HOST_ACCESS_DOCKER_SOCKET_ENABLED", "CLAUDE_SANDBOX_HOST_ACCESS_AWS_ENABLED",
	"CLAUDE_SANDBOX_HOST_ACCESS_PACKAGE_CACHES_ENABLED",
}

// LaunchRecord is what a launch records for a later restore.
type LaunchRecord struct {
	// Model is the --model given on the command line ("" when none): claude's
	// own --model after "--" when given (it comes later and wins in claude),
	// else the launcher's.
	Model string
	// ClaudeModel is true when claude's own --model was given. The container's
	// model label then does not name the session's model, so an attach must
	// not take it from there (CS-TMUX-012).
	ClaudeModel bool
	// Replay is the allowlisted claude flags with their values, as given.
	Replay []string
	// Unreplayed names every other flag given at launch that a restore will
	// not pass. Names only.
	Unreplayed []string
	// Name is the conversation name the passthrough gives claude (--name),
	// "" when none: the window label's source (CS-TMUX-021). Never part of
	// the launchflags label.
	Name string
}

// Record classifies a launch: launcherGiven is the canonical names of the
// launcher flags given on the command line, model the command-line --model,
// passthrough the claude arguments as given (CS-TMUX-013).
func Record(launcherGiven []string, model string, passthrough []string, getenv func(string) string) LaunchRecord {
	r := LaunchRecord{Model: model, Name: NameArg(passthrough)}
	for _, n := range launcherGiven {
		if slices.Contains(LauncherNamed, n) {
			r.Unreplayed = appendName(r.Unreplayed, n)
		}
	}
	if getenv != nil {
		for _, k := range EnvNamed {
			if getenv(k) != "" {
				r.Unreplayed = appendName(r.Unreplayed, k)
			}
		}
	}
	replay, named, ptModel := ScanPassthrough(passthrough)
	r.Replay = replay
	if ptModel != "" {
		// claude's own --model after the launcher's: the later one wins in
		// claude, and it is as much the session's model (identity, never
		// widening).
		r.Model, r.ClaudeModel = ptModel, true
	}
	for _, n := range named {
		r.Unreplayed = appendName(r.Unreplayed, n)
	}
	return r
}

// LabelNames is the claude-sandbox.launchflags label's content (CS-LNCH-109):
// names only — "--model" when only the launcher's --model was given (the
// model label then names the session's model), "--model:claude" when claude's
// own was (it does not), the replayed flags' names, then the unreplayed
// names. An attach, which never saw the values, reads it back with FromLabel.
func (r LaunchRecord) LabelNames() []string {
	var out []string
	switch {
	case r.ClaudeModel:
		out = append(out, LabelClaudeModel)
	case r.Model != "":
		out = append(out, "--model")
	}
	for _, t := range r.Replay {
		if !strings.HasPrefix(t, "-") {
			continue // a value
		}
		if name, _ := flagName(t); isAllowed(name) {
			out = appendName(out, name)
		}
	}
	for _, n := range r.Unreplayed {
		out = appendName(out, n)
	}
	return out
}

// LabelValue renders LabelNames for the label.
func (r LaunchRecord) LabelValue() string { return strings.Join(r.LabelNames(), ",") }

// labelName is what a launchflags label entry may look like; anything else
// is dropped when read back.
var labelName = regexp.MustCompile(`^(--?[A-Za-z0-9][A-Za-z0-9-]*|[A-Z][A-Z0-9_]*)$`)

// LabelClaudeModel is the launchflags entry for a claude --model given after
// "--": the model label holds the launcher's model, not the session's.
const LabelClaudeModel = "--model:claude"

// FromLabel reads a launchflags label (CS-TMUX-012): whether --model was
// given, and every other name, which for an attach is all unreplayed.
func FromLabel(v string) (modelGiven bool, unreplayed []string) {
	for _, n := range strings.Split(v, ",") {
		n = strings.TrimSpace(n)
		if n == LabelClaudeModel {
			// Unknown value: named, and the model label is not the model.
			unreplayed = appendName(unreplayed, "--model")
			continue
		}
		if n == "" || !labelName.MatchString(n) {
			continue
		}
		if n == "--model" {
			modelGiven = true
			continue
		}
		unreplayed = appendName(unreplayed, n)
	}
	return modelGiven, unreplayed
}

// ScanPassthrough walks claude's arguments (plan 08 § 4): it stops at "--" or
// the first positional, never reads a known flag's value as a positional,
// and after an unknown flag followed by a non-flag token it stops, since the
// arity is unknown (that can only miss flags, which fails safe). It returns
// the allowlisted flags with their values, the names of every other flag
// outside the session family and --model, and claude's own --model value.
func ScanPassthrough(args []string) (replay, named []string, model string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			return replay, named, model
		}
		name, hasValue := flagName(a)
		ar, allowed := ReplayAllowlist[name]
		known := allowed
		if !known {
			if x, ok := claudeArity[name]; ok {
				ar, known = x, true
			} else if claudeBoolean[name] {
				ar, known = noValue, true
			}
		}
		// The tokens this flag spans: itself plus its values.
		n := 1
		if !hasValue {
			switch ar {
			case oneValue:
				if i+1 < len(args) {
					n = 2
				}
			case optionalValue:
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					n = 2
				}
			case variadic:
				for i+n < len(args) && !strings.HasPrefix(args[i+n], "-") {
					n++
				}
			}
		}
		switch {
		case allowed:
			replay = append(replay, args[i:i+n]...)
		case sessionFamily[name]:
		case name == "--model":
			if hasValue {
				_, model, _ = strings.Cut(a, "=")
			} else if n == 2 {
				model = args[i+1]
			}
		default:
			named = appendName(named, name)
		}
		if !known && !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			// An unknown flag with a following word: value or prompt, the
			// scan cannot tell.
			return replay, named, model
		}
		i += n
	}
	return replay, named, model
}

// shortWithValue matches "-r<id>"-style glued short options.
var shortWithValue = regexp.MustCompile(`^(-[A-Za-z])(.+)$`)

// flagName returns a flag token's name ("--x" of "--x=v", "-r" of "-rID") and
// whether the token already carries its value.
func flagName(t string) (string, bool) {
	if strings.HasPrefix(t, "--") {
		name, _, has := strings.Cut(t, "=")
		return name, has
	}
	if m := shortWithValue.FindStringSubmatch(t); m != nil {
		return m[1], true
	}
	return t, false
}

// ResumeID returns the conversation a passthrough resumes explicitly —
// "--resume <id>", "--resume=<id>", "-r <id>", "-r<id>" with a canonical
// UUID — scanning with ScanPassthrough's stop rules (plan 07 § 6, 08 § 4).
// claude's parser keeps the LAST of several, so the scan does too, up to the
// stop (CS-LNCH-110); "" when that last one has no id or a non-UUID one.
func ResumeID(args []string) string {
	last := ""
	for i := 0; i < len(args); {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		name, hasValue := flagName(a)
		if name == "--resume" || name == "-r" {
			// [value]: glued, or the next token unless it is a flag.
			switch {
			case hasValue && name == "--resume":
				_, last, _ = strings.Cut(a, "=")
				i++
			case hasValue:
				last = a[2:]
				i++
			case i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"):
				last = args[i+1]
				i += 2
			default:
				last = ""
				i++
			}
			continue
		}
		next, stop := step(args, i)
		if stop {
			break
		}
		i = next
	}
	if registry.IsUUID(last) {
		return strings.ToLower(last)
	}
	return ""
}

// ForkSession reports whether a passthrough asks claude to fork
// ("--fork-session") before the scan's stop (plan 08 § 4): one inside a
// prompt or after "--" does not count.
func ForkSession(args []string) bool {
	for i := 0; i < len(args); {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			return false
		}
		if name, _ := flagName(a); name == "--fork-session" {
			return true
		}
		next, stop := step(args, i)
		if stop {
			return false
		}
		i = next
	}
	return false
}

// GuardedResumeID is the conversation the resume guard protects for a launch
// (CS-LNCH-110, CS-SESS-065): the explicit ResumeID, unless the passthrough
// forks it — a fork gets a new id, so it opens nothing already open. The
// claude-sandbox.resume label carries it.
func GuardedResumeID(args []string) string {
	if ForkSession(args) {
		return ""
	}
	return ResumeID(args)
}

// step returns the index after the flag token at i and its values, and
// whether the scan must stop there: an unknown flag followed by a word, whose
// arity the scan cannot know.
func step(args []string, i int) (next int, stop bool) {
	name, hasValue := flagName(args[i])
	if hasValue {
		return i + 1, false
	}
	ar, ok := ReplayAllowlist[name]
	if !ok {
		if ar, ok = claudeArity[name]; !ok {
			if !claudeBoolean[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return i + 1, true
			}
			return i + 1, false
		}
	}
	switch ar {
	case oneValue:
		i++
	case optionalValue:
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	case variadic:
		for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return i + 1, false
}

func isAllowed(name string) bool {
	_, ok := ReplayAllowlist[name]
	return ok
}

func appendName(list []string, n string) []string {
	if slices.Contains(list, n) {
		return list
	}
	return append(list, n)
}
