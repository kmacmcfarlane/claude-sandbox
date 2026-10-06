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

// claudeArity is the value arity of the claude flags that take one, copied
// from Claude Code 2.1.290's root command definition (its commander
// .option / new Option calls, hidden options included), so no scan mistakes
// a value for a positional. A flag absent from here and from ReplayAllowlist
// is a boolean when claudeBoolean lists it, else unknown. Refresh both tables
// on a Claude Code bump (CS-LNCH-183). Table drift is WalkClaudeArgs's only
// way to fail open, in two shapes (plan continue-resume-guard 01 § 2.2, R3):
//   - a flag listed as boolean or optional that a later Claude Code makes
//     take a required value, followed by "--fork-session" (counted, not made)
//     or a later resume (claude takes it as the value);
//   - a listed flag that a later Claude Code makes value-less
//     ("--resume <a> --name -r <b>"), or a new value-less flag the table
//     lacks ("--resume <a> --newbool -r <b>"): the later resume reads as
//     uncertain, so the guard checks <a> while claude resumes <b>.
var claudeArity = map[string]arity{
	"--add-dir": variadic, "--advisor": oneValue, "--agent": oneValue,
	"--agent-color": oneValue, "--agent-id": oneValue, "--agent-name": oneValue,
	"--agent-type": oneValue, "--agents": oneValue,
	"--allowedTools": variadic, "--allowed-tools": variadic,
	"--append-subagent-system-prompt": oneValue, "--append-subagent-system-prompt-file": oneValue,
	"--append-system-prompt": oneValue, "--append-system-prompt-file": oneValue,
	"--attach-serve": oneValue, "--autocompact": oneValue, "--betas": variadic,
	"--channels": variadic, "--client-data-url": oneValue, "--cloud": optionalValue,
	"--correlation-id": oneValue, "--dangerously-load-development-channels": variadic,
	"--debug": optionalValue, "-d": optionalValue, "--debug-file": oneValue,
	"--deep-link-cwd-b64": oneValue, "--deep-link-last-fetch": oneValue,
	"--deep-link-repo":  oneValue,
	"--disallowedTools": variadic, "--disallowed-tools": variadic,
	"--effort": oneValue, "--environment": oneValue, "--fallback-model": oneValue,
	"--file": variadic, "--forward-home-settings": oneValue, "--from-pr": optionalValue,
	"--inherit-permission-mode": oneValue, "--input-format": oneValue,
	"--json-schema": oneValue, "--managed-settings": oneValue,
	"--max-budget-usd": oneValue, "--max-thinking-tokens": oneValue, "--max-turns": oneValue,
	"--mcp-config": variadic, "--messaging-socket-path": oneValue, "--model": oneValue,
	"--name": oneValue, "-n": oneValue, "--on-branch": oneValue,
	"--output-format": oneValue, "--parent-session-id": oneValue,
	"--permission-mode": oneValue, "--permission-prompt-tool": oneValue,
	"--permission-prompts": oneValue, "--plan-mode-instructions": oneValue,
	"--plugin-dir": oneValue, "--plugin-dir-no-mcp": oneValue, "--plugin-url": oneValue,
	"--pool": oneValue, "--prefill": oneValue, "--prefill-b64": oneValue,
	"--proactivity": oneValue, "--project-config-root": oneValue,
	"--prompt-suggestions": optionalValue, "--rc": optionalValue, "--ref": oneValue,
	"--remote": optionalValue, "--remote-control": optionalValue,
	"--remote-control-session-name-prefix": oneValue,
	"--resume":                             optionalValue, "-r": optionalValue,
	"--resume-drops-turn": oneValue, "--resume-session-at": oneValue,
	"--rewind-files": oneValue, "--sdk-url": oneValue, "--session-id": oneValue,
	"--setting-sources": oneValue, "--settings": oneValue, "--system-prompt": oneValue,
	"--system-prompt-file": oneValue, "--system-prompt-snapshot": oneValue,
	"--task-budget": oneValue, "--team-name": oneValue, "--teammate-mode": oneValue,
	"--teleport": optionalValue, "--thinking": oneValue, "--thinking-display": oneValue,
	"--tools": variadic, "--watch-artifact": oneValue,
	"--watch-artifact-no-autoreact": oneValue, "--workload": oneValue,
	"--worktree": optionalValue, "-w": optionalValue,
}

// claudeBoolean is the known claude flags that take no value (Claude Code
// 2.1.290, the same source as claudeArity; -h/--help and -v/--version come
// from commander's helpOption and version).
var claudeBoolean = map[string]bool{
	"--allow-dangerously-skip-permissions": true, "--await-claim": true,
	"--await-initialize": true, "--ax-screen-reader": true, "--bare": true,
	"--bg": true, "--background": true, "--brief": true, "--chrome": true,
	"--continue": true, "-c": true, "--dangerously-skip-permissions": true,
	"-d2e": true, "--debug-to-stderr": true, "--deep-link-origin": true,
	"--desktop": true, "--disable-slash-commands": true, "--enable-auth-status": true,
	"--enable-auto-mode": true, "--exclude-dynamic-system-prompt-sections": true,
	"--fork-session": true, "--forward-subagent-text": true, "--ide": true,
	"--include-hook-events": true, "--include-partial-messages": true,
	"--init": true, "--init-only": true, "--maintenance": true,
	"--no-chrome": true, "--no-session-persistence": true,
	"--plan-mode-required": true, "--print": true, "-p": true,
	"--replay-user-messages": true, "--reply-on-resume": true, "--restricted": true,
	"--safe-mode": true, "--session-mirror": true, "--strict-mcp-config": true,
	"--tmux": true, "--verbose": true,
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
// stop; "" when that last one has no id or a non-UUID one. It feeds the pane
// mark (CS-TMUX-011/017), where a missed id only means the save hook resolves
// the conversation from the registry. The resume guard reads
// GuardedResumeID, which follows claude's own parser (CS-LNCH-183).
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

// ForkSession reports whether a passthrough certainly asks claude to fork
// ("--fork-session"), as WalkClaudeArgs reads it (CS-LNCH-183): one after a
// stopping "--", in a value-consuming position, right after an unknown flag
// or in an uncertain tail does not count.
func ForkSession(args []string) bool { return WalkClaudeArgs(args).Fork }

// GuardedResumeID is the conversation the resume guard protects for a launch
// (CS-LNCH-110, CS-SESS-065): WalkClaudeArgs's resume id, unless the
// passthrough certainly forks — a fork gets a new id, so it opens nothing
// already open. The claude-sandbox.resume label carries it. It follows
// claude's own option parser (CS-LNCH-183), not ResumeID's stop rules, so a
// resume after a prompt word or inside a short cluster is guarded.
func GuardedResumeID(args []string) string {
	w := WalkClaudeArgs(args)
	if w.Fork {
		return ""
	}
	return w.ResumeID
}

// GuardedContinue reports whether a passthrough continues the newest
// conversation without certainly forking it (CS-LNCH-183): the input the
// continue guard (item 3ce1) will read. Nothing calls it yet.
func GuardedContinue(args []string) bool {
	w := WalkClaudeArgs(args)
	return w.Continue && !w.Fork
}

// ClaudeArgs is what claude's own option parser (commander, Claude Code
// 2.1.290) would see in a passthrough, read so that every guard-relevant
// mistake over-refuses (CS-LNCH-183).
type ClaudeArgs struct {
	// Continue: --continue / -c / a short cluster reaching c, wherever it
	// appears before the stop (a flag's value included: over-refusal).
	Continue bool
	// ResumeGiven: any --resume / -r form before the stop.
	ResumeGiven bool
	// ResumeID is the resumed conversation when a canonical UUID (lower
	// case), else "". A resume in a certain position replaces it (claude's
	// last one wins, "" for none or a non-UUID); one in an uncertain position
	// only sets it while it is still "".
	ResumeID string
	// Fork: --fork-session where it is certainly an option.
	Fork bool
	// Print: -p / --print / a cluster reaching p, read as an option.
	Print bool
	// Worktree is the last -w / --worktree value read as an option ("" when
	// bare); one in an uncertain position (right after an unknown flag, in an
	// uncertain tail) only sets it while it is still "", as for ResumeID.
	// WorktreeGiven says one was given.
	Worktree      string
	WorktreeGiven bool
}

// claudeFlag is one option a token stands for.
type claudeFlag struct {
	name     string
	value    string
	hasValue bool // the value came with the token (glued, "=", rest of a cluster)
	badEq    bool // not an option claude acts on: an unknown flag, or "--x=v" for one that takes no value
}

// optionLike is commander's test for an option token: longer than one
// character and starting with "-" (a lone "-" is a value or operand).
func optionLike(t string) bool { return len(t) > 1 && t[0] == '-' }

// claudeFlagArity is a flag's arity and whether claude 2.1.290 knows it.
func claudeFlagArity(name string) (arity, bool) {
	if ar, ok := ReplayAllowlist[name]; ok {
		return ar, true
	}
	if ar, ok := claudeArity[name]; ok {
		return ar, true
	}
	if claudeBoolean[name] {
		return noValue, true
	}
	return noValue, false
}

// interpretToken expands one option-like token the way commander's
// parseOptions does: an exact known flag; else a short cluster, each known
// boolean short set and the rest re-read as "-<rest>", a value-taking short
// taking the rest of the cluster as its value; else a "--name=value". pending
// is the last flag when it still takes its value from the next token (a
// value-taking flag or short written last), "" otherwise; unknown is true for
// an unknown flag written without "=" (its arity cannot be known).
func interpretToken(tok string) (flags []claudeFlag, pending string, unknown bool) {
	t := tok
	for {
		if ar, ok := claudeFlagArity(t); ok {
			if ar == noValue {
				return append(flags, claudeFlag{name: t}), "", false
			}
			return flags, t, false
		}
		if len(t) > 2 && t[1] != '-' {
			short := t[:2]
			if ar, ok := claudeFlagArity(short); ok {
				if ar != noValue {
					// required, or optional with commander's default
					// combineFlagAndOptionalValue: the rest is the value.
					return append(flags, claudeFlag{name: short, value: t[2:], hasValue: true}), "", false
				}
				flags = append(flags, claudeFlag{name: short})
				t = "-" + t[2:]
				continue
			}
		}
		if strings.HasPrefix(t, "--") {
			if name, value, has := strings.Cut(t, "="); has {
				ar, ok := claudeFlagArity(name)
				return append(flags, claudeFlag{name: name, value: value, hasValue: true,
					badEq: !ok || ar == noValue}), "", false
			}
		}
		return append(flags, claudeFlag{name: t, badEq: true}), "", true
	}
}

func isContinueFlag(n string) bool { return n == "--continue" || n == "-c" }
func isResumeFlag(n string) bool   { return n == "--resume" || n == "-r" }

// WalkClaudeArgs reads a passthrough the way claude's own option parser
// (commander's parseOptions in Claude Code 2.1.290) would, erring toward the
// guards' refusal wherever it cannot be sure (CS-LNCH-183; plan
// continue-resume-guard 01 § 2.2, 02 § 3, 03 §§ 1-2):
//
//   - Positional words never stop the walk: commander keeps parsing options
//     after an operand. Only a bare "--" that nothing consumes stops it.
//   - A token right after a required-value flag, a variadic flag's FIRST
//     value, or a short cluster ending in a required-value short — each
//     written without "=" — is in a value-consuming position: it is that
//     flag's value, so "--" there does not stop, "--fork-session" there is no
//     fork, and a resume there is uncertain.
//   - Right after an unknown flag written without "=" the next token is
//     uncertain; a "--" there starts an uncertain tail that runs to the end.
//     In both, continue and resume still count, a fork never does.
//   - --continue and --resume count wherever they appear (a value included).
//   - An uncertain resume never replaces or clears an id already set.
func WalkClaudeArgs(args []string) ClaudeArgs {
	var w ClaudeArgs
	resume := func(value string, uncertain bool) {
		w.ResumeGiven = true
		id := ""
		if registry.IsUUID(value) {
			id = strings.ToLower(value)
		}
		switch {
		case !uncertain:
			w.ResumeID = id // claude's last resume wins
		case w.ResumeID == "":
			w.ResumeID = id // uncertain: never replaces or clears one
		}
	}
	// worktree follows the resume id's rule: an uncertain -w never replaces
	// or clears a value already set.
	worktree := func(value string, uncertain bool) {
		w.WorktreeGiven = true
		if !uncertain || w.Worktree == "" {
			w.Worktree = value
		}
	}
	// peek is the token after i when an optional value would take it.
	peek := func(i int) (string, bool) {
		if i+1 < len(args) && !optionLike(args[i+1]) {
			return args[i+1], true
		}
		return "", false
	}

	const (
		posCertain      = iota
		posValue        // a value-consuming position
		posAfterUnknown // right after an unknown flag without "="
	)
	pos := posCertain
	valueOfVariadic := false // the value-consuming flag is variadic
	variadicOn := false      // later non-option tokens are a variadic's values
	tail := false            // an uncertain tail: "--" after an unknown flag
	for i := 0; i < len(args); i++ {
		t := args[i]
		if pos == posValue {
			// The flag's value, whatever it is. Only continue and resume
			// count here (rule 2): an uncertain resume.
			if optionLike(t) && t != "--" {
				flags, pending, _ := interpretToken(t)
				for _, f := range flags {
					switch {
					case isContinueFlag(f.name):
						w.Continue = true
					case isResumeFlag(f.name) && !f.badEq:
						resume(f.value, true)
					}
				}
				if isResumeFlag(pending) {
					v, _ := peek(i)
					resume(v, true)
				}
			}
			pos, variadicOn = posCertain, valueOfVariadic
			continue
		}
		if t == "--" {
			switch {
			case pos == posAfterUnknown:
				tail = true
			case tail:
				// Never stops in an uncertain tail.
			default:
				return w // a real stop
			}
			pos = posCertain
			continue
		}
		if variadicOn && !optionLike(t) {
			continue // a variadic's later value
		}
		variadicOn = false
		uncertain := tail || pos == posAfterUnknown
		pos = posCertain
		if !optionLike(t) {
			continue // an operand
		}
		flags, pending, unknown := interpretToken(t)
		for _, f := range flags {
			switch {
			case isContinueFlag(f.name):
				w.Continue = true
			case f.badEq:
				// Unknown, or a value claude rejects.
			case isResumeFlag(f.name):
				resume(f.value, uncertain)
			case f.name == "--fork-session":
				if !uncertain {
					w.Fork = true
				}
			case f.name == "--print" || f.name == "-p":
				w.Print = true
			case f.name == "--worktree" || f.name == "-w":
				worktree(f.value, uncertain)
			}
		}
		if unknown {
			pos = posAfterUnknown
			continue
		}
		if pending == "" {
			continue
		}
		switch ar, _ := claudeFlagArity(pending); ar {
		case oneValue, variadic:
			// commander shifts the next token unconditionally.
			if i+1 < len(args) {
				pos, valueOfVariadic = posValue, ar == variadic
			}
		case optionalValue:
			v, took := peek(i)
			switch {
			case isResumeFlag(pending):
				resume(v, uncertain)
			case pending == "--worktree" || pending == "-w":
				worktree(v, uncertain)
			}
			if took {
				i++
			}
		}
	}
	return w
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
