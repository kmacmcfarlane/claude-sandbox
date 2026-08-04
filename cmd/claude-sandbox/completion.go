package main

// Spec: spec/completion.feature (CS-COMP) — shell completion.
//
// The root command sets DisableFlagParsing, which has two consequences cobra
// documents but that are easy to trip over:
//
//   - cobra knows none of the launcher's flags (they live in scanLaunchArgs),
//     so it can complete none of them;
//   - checkIfFlagCompletion() short-circuits on DisableFlagParsing, so
//     RegisterFlagCompletionFunc never fires for a root flag value.
//
// Root therefore does all of its own completion in completeLaunch(), which
// receives the raw (unstripped) command line. Subcommands parse normally and
// get cobra's built-in flag completion for free.

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// modelAliases are the short names accepted by --model. A full model ID is
// equally valid but is free text, so it is not enumerated.
var modelAliases = []string{"opus", "sonnet", "haiku"}

// valueKind describes what, if anything, follows a launcher flag.
type valueKind int

const (
	valueNone valueKind = iota
	valueModel
	valueOpaque // takes a value that cannot be usefully completed
)

// launchFlagSpec describes one launcher flag for completion purposes.
//
// This table is the completion-side view of scanLaunchArgs; CS-COMP-020 and
// CS-COMP-021 assert that it stays in step with the parser and with
// launchUsage respectively.
type launchFlagSpec struct {
	Name  string
	Desc  string // "" marks a verbose alias, which is parsed but not offered
	Value valueKind
}

func (s launchFlagSpec) alias() bool { return s.Desc == "" }

var launchFlagSpecs = []launchFlagSpec{
	{Name: "--help", Desc: "Show this help message and exit"},
	{Name: "-h"},
	{Name: "--version", Desc: "Show claude-sandbox version (host + baked image) and exit"},
	{Name: "--ralph", Desc: "Launch the ralph loop runner instead of interactive claude"},
	{Name: "--limit", Desc: "Run ralph for N iterations (only valid with --ralph)", Value: valueOpaque},
	{Name: "--model", Desc: "Model to use (alias like 'opus' or a full model ID)", Value: valueModel},
	{Name: "--dangerous", Desc: "Skip permission prompts (--dangerously-skip-permissions)"},
	{Name: "--dangerously-skip-permissions"},
	{Name: "--rebuild", Desc: "Force rebuild of base and child images"},
	{Name: "--update", Desc: "Auto-accept the Claude Code update rebuild prompt"},
	{Name: "--no-update-check", Desc: "Skip Claude Code version check"},
	{Name: "--docker-socket", Desc: "Mount the host Docker socket into the container"},
	{Name: "--host-access-docker-socket-enabled"},
	{Name: "--aws", Desc: "Mount ~/.aws/ read-only into the container"},
	{Name: "--host-access-aws-enabled"},
	{Name: "--git", Desc: "Mount ~/.gitconfig read-only into the container"},
	{Name: "--host-access-git-enabled"},
	{Name: "--ssh", Desc: "Mount ~/.ssh/ read-only into the container"},
	{Name: "--host-access-ssh-enabled"},
}

func lookupLaunchFlag(name string) (launchFlagSpec, bool) {
	for _, s := range launchFlagSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return launchFlagSpec{}, false
}

// completeLaunch is the root command's ValidArgsFunction (CS-COMP-004..013).
//
// args is the command line *before* the word being completed. Because root
// disables flag parsing, cobra hands it over unstripped, which is exactly what
// the launcher grammar needs.
func completeLaunch(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	// A value position: the previous word is a flag that takes one.
	if len(args) > 0 {
		if s, ok := lookupLaunchFlag(args[len(args)-1]); ok {
			switch s.Value {
			case valueModel:
				return prefixed(modelAliases, toComplete), cobra.ShellCompDirectiveNoFileComp
			case valueOpaque:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
	}

	// Replay the real grammar so the passthrough boundary can never drift.
	f, err := scanLaunchArgs(args)
	if err != nil {
		// The command line is already invalid (unknown flag); offering more
		// would be misleading. CS-COMP-013.
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Passthrough is nil only while no boundary has been crossed: every branch
	// that ends launcher parsing assigns a slice, including the "--" branch,
	// whose remainder may legitimately be empty. CS-COMP-010..012.
	if f.Passthrough != nil {
		// Past the boundary the arguments belong to claude, which owns its own
		// completion; fall back to the shell's default (paths).
		return nil, cobra.ShellCompDirectiveDefault
	}

	if !strings.HasPrefix(toComplete, "-") {
		// A positional would itself start the passthrough. Cobra has already
		// offered the subcommand names if this is argv[1].
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []cobra.Completion
	seen := map[string]bool{}
	for _, s := range launchFlagSpecs {
		if s.alias() || !strings.HasPrefix(s.Name, toComplete) {
			continue
		}
		seen[s.Name] = true
		comps = append(comps, cobra.CompletionWithDesc(s.Name, s.Desc))
	}
	// Known claude flags are accepted here too, and typing one is how the
	// passthrough tail is opened. CS-COMP-009.
	for name := range knownPassthrough {
		if seen[name] || !strings.HasPrefix(name, toComplete) {
			continue
		}
		comps = append(comps, cobra.CompletionWithDesc(name, "passed through to claude"))
	}
	slices.Sort(comps)
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func prefixed(candidates []string, toComplete string) []cobra.Completion {
	var out []cobra.Completion
	for _, c := range candidates {
		if strings.HasPrefix(c, toComplete) {
			out = append(out, c)
		}
	}
	return out
}

// completeModelFlag is the RegisterFlagCompletionFunc form, for subcommands
// that do parse their flags normally.
func completeModelFlag(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return prefixed(modelAliases, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// registerRalphCompletions annotates ralph's flag values (CS-COMP-015/016/019).
// Path-valued flags are left alone: cobra's default directive already yields
// file completion.
func registerRalphCompletions(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("model", completeModelFlag)
	for _, name := range []string{
		"limit", "watchdog-timeout", "iteration-timeout",
		"max-retries", "retry-delay", "quota-pause", "quota-max-wait",
	} {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
}
