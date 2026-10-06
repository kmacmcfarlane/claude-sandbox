package tmuxpane_test

// Spec: spec/launch.feature CS-LNCH-183 — the shared walker over claude's
// arguments (plan continue-resume-guard 01 § 2.2, 02 § 3, 03 §§ 1-2): what
// commander's parseOptions in Claude Code 2.1.290 would see, read so that
// every guard-relevant mistake over-refuses.

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("CS-LNCH-183: WalkClaudeArgs", func() {
	const (
		a = "53cd0872-ec39-41a3-86bd-b000abb5fb32"
		b = "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	)
	type CA = tmuxpane.ClaudeArgs
	sp := func(s string) []string { return strings.Fields(s) }

	DescribeTable("CS-LNCH-183: walker rows",
		func(args []string, want CA) {
			Expect(tmuxpane.WalkClaudeArgs(args)).To(Equal(want), "%q", args)
		},
		// Rule 1: positionals never stop the walk; only a bare "--" does.
		Entry("a resume after a prompt word", []string{"fix it", "--resume", convID},
			CA{ResumeGiven: true, ResumeID: convID}),
		Entry("a continue after a prompt word", []string{"prompt", "--continue"}, CA{Continue: true}),
		Entry("-- then --continue stops", sp("-- --continue"), CA{}),
		Entry("-- then --resume stops", []string{"--", "--resume", convID}, CA{}),
		// Rule 4: short clusters expand as commander expands them.
		Entry("-pr <uuid>", []string{"-pr", convID}, CA{ResumeGiven: true, ResumeID: convID, Print: true}),
		Entry("-pc", sp("-pc"), CA{Continue: true, Print: true}),
		Entry("-cp", sp("-cp"), CA{Continue: true, Print: true}),
		Entry("-rc resumes the name c", sp("-rc"), CA{ResumeGiven: true}),
		Entry("-r<uuid> glued", []string{"-r" + strings.ToUpper(convID)}, CA{ResumeGiven: true, ResumeID: convID}),
		Entry("-w name", sp("-w feat"), CA{Worktree: "feat", WorktreeGiven: true}),
		Entry("-pwfeat", sp("-pwfeat"), CA{Print: true, Worktree: "feat", WorktreeGiven: true}),
		Entry("bare -w before a flag", sp("-w --verbose"), CA{WorktreeGiven: true}),
		// Rule 2: continue and resume count wherever they appear.
		Entry("--append-system-prompt --continue is counted", sp("--append-system-prompt --continue"), CA{Continue: true}),
		Entry("--verbose --continue", sp("--verbose --continue"), CA{Continue: true}),
		Entry("--continue then a fork", sp("--continue x --fork-session"), CA{Continue: true, Fork: true}),
		Entry("a fork after a prompt word", sp("prompt --fork-session"), CA{Fork: true}),
		// Rule 3: a fork counts only where it is certainly an option.
		Entry("--effort high --fork-session forks", sp("--effort high --fork-session"), CA{Fork: true}),
		Entry("--unknownflag --fork-session does not", sp("--unknownflag --fork-session"), CA{}),
		Entry("--name --fork-session is a name", sp("--name --fork-session"), CA{}),
		Entry("--fork-session=x is rejected by claude", sp("--fork-session=x"), CA{}),
		// 02 § 3.3: "--" consumed as a value, the uncertain tail.
		Entry("--system-prompt -- --continue", sp("--system-prompt -- --continue"), CA{Continue: true}),
		Entry("-pn -- --continue (-n consumes --)", sp("-pn -- --continue"), CA{Continue: true, Print: true}),
		Entry("--verbose -- --continue (a real stop)", sp("--verbose -- --continue"), CA{}),
		Entry("--unknownflag -- --continue --fork-session (uncertain tail)",
			sp("--unknownflag -- --continue --fork-session"), CA{Continue: true}),
		Entry("an uncertain tail never stops", sp("--unknownflag -- -- --continue"), CA{Continue: true}),
		// 02 § 3.2: which resume id wins.
		Entry("--resume <uuid> --name -r keeps the id", []string{"--resume", convID, "--name", "-r"},
			CA{ResumeGiven: true, ResumeID: convID}),
		Entry("--name --resume <uuid>, nothing before", []string{"--name", "--resume", convID},
			CA{ResumeGiven: true, ResumeID: convID}),
		Entry("--resume <a> --resume <b>", []string{"--resume", a, "--resume", b}, CA{ResumeGiven: true, ResumeID: b}),
		Entry("--resume <a> --resume (the picker last)", []string{"--resume", a, "--resume"}, CA{ResumeGiven: true}),
		Entry("--resume <a> --unknownflag -r <b>", []string{"--resume", a, "--unknownflag", "-r", b},
			CA{ResumeGiven: true, ResumeID: a}),
		Entry("--resume <a> --resume words", []string{"--resume", a, "--resume", "words"}, CA{ResumeGiven: true}),
		Entry("--resume words gives no id", sp("--resume words"), CA{ResumeGiven: true}),
		Entry("-rc after an id clears it", []string{"--resume", a, "-rc"}, CA{ResumeGiven: true}),
		// 03 § 1: a variadic's first value is consumed like a required one.
		Entry("--add-dir --fork-session --continue", sp("--add-dir --fork-session --continue"), CA{Continue: true}),
		Entry("--add-dir -- --continue", sp("--add-dir -- --continue"), CA{Continue: true}),
		// An intended over-refusal: claude reads -r and the id as tool
		// names, the walker checks the id.
		Entry("--tools -r <uuid>, nothing before", []string{"--tools", "-r", convID},
			CA{ResumeGiven: true, ResumeID: convID}),
		Entry("--resume <a> --mcp-config -r", []string{"--resume", a, "--mcp-config", "-r"},
			CA{ResumeGiven: true, ResumeID: a}),
		Entry("--add-dir d1 d2 --fork-session", sp("--add-dir d1 d2 --fork-session"), CA{Fork: true}),
		Entry("--add-dir d1 -- --continue (a real stop)", sp("--add-dir d1 -- --continue"), CA{}),
		Entry("--add-dir=d1 then a word then a fork", sp("--add-dir=d1 w --fork-session"), CA{Fork: true}),
		// Review rows (commander-verified).
		Entry("-d2e is an exact boolean match", sp("-d2e --fork-session"), CA{Fork: true}),
		Entry("-pnfoo: -n takes the rest of the cluster", sp("-pnfoo --fork-session"), CA{Fork: true, Print: true}),
		Entry("-pX ends in an unknown short", sp("-pX --fork-session"), CA{Print: true}),
		Entry("--resume <a> --name --resume <b>", []string{"--resume", a, "--name", "--resume", b},
			CA{ResumeGiven: true, ResumeID: a}),
		Entry("--add-dir d1 --resume <uuid>", []string{"--add-dir", "d1", "--resume", convID},
			CA{ResumeGiven: true, ResumeID: convID}),
		Entry("--unk=v consumes nothing", sp("--unk=v --fork-session"), CA{Fork: true}),
		Entry("--resume= then a fork", sp("--resume= --fork-session"), CA{ResumeGiven: true, Fork: true}),
		Entry("--resume - (a lone dash is a value)", sp("--resume -"), CA{ResumeGiven: true}),
		Entry("--name -p is a name", sp("--name -p"), CA{}),
		// The worktree follows the resume id's rule.
		Entry("-w a -w b", sp("-w a -w b"), CA{Worktree: "b", WorktreeGiven: true}),
		Entry("-w a --unk -w b keeps a", sp("-w a --unk -w b"), CA{Worktree: "a", WorktreeGiven: true}),
		Entry("-w a --unk -- -w b keeps a", sp("-w a --unk -- -w b"), CA{Worktree: "a", WorktreeGiven: true}),
		Entry("-w a --unk -w keeps a", sp("-w a --unk -w"), CA{Worktree: "a", WorktreeGiven: true}),
		Entry("--unk -w b, nothing before", sp("--unk -w b"), CA{Worktree: "b", WorktreeGiven: true}),
		// The 2.1.290 table: flags added since 2.1.284 take their value.
		Entry("--thinking -- --continue", sp("--thinking -- --continue"), CA{Continue: true}),
		Entry("--prefill --fork-session is a prefill", sp("--prefill --fork-session"), CA{}),
	)

	It("CS-LNCH-183: GuardedContinue is a continue that does not certainly fork", func() {
		Expect(tmuxpane.GuardedContinue(sp("prompt -c"))).To(BeTrue())
		Expect(tmuxpane.GuardedContinue(sp("--continue --fork-session"))).To(BeFalse())
		Expect(tmuxpane.GuardedContinue(sp("--frobnicate --fork-session --continue"))).To(BeTrue())
		Expect(tmuxpane.GuardedContinue(sp("-- --continue"))).To(BeFalse())
	})
})
