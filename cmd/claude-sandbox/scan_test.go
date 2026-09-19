package main

// Spec: spec/launch.feature — launcher argument scanning (CS-LNCH-001..005).
// scanLaunchArgs is exercised directly; end-to-end argv assertions live in
// launch_cli_test.go.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

var _ = Describe("scanLaunchArgs", func() {
	It("CS-LNCH-001: accepts every launcher flag and alias", func() {
		cases := [][]string{
			{"--help"}, {"-h"}, {"--version"}, {"--ralph"},
			{"--limit", "5"}, {"--model", "opus"},
			{"--dangerous"}, {"--dangerously-skip-permissions"},
			{"--rebuild"}, {"--update"}, {"--no-update-check"},
			{"--ssh"}, {"--host-access-ssh-enabled"},
			{"--git"}, {"--host-access-git-enabled"},
			{"--docker-socket"}, {"--host-access-docker-socket-enabled"},
			{"--aws"}, {"--host-access-aws-enabled"},
			{"--worktree"}, {"--worktree=feature-x"}, {"--no-worktree"},
		}
		for _, args := range cases {
			f, err := scanLaunchArgs(args)
			Expect(err).NotTo(HaveOccurred(), "args: %v", args)
			Expect(f.Passthrough).To(BeEmpty(), "args: %v", args)
		}
	})

	It("CS-LNCH-001: flag values land in the scanned flag set", func() {
		f, err := scanLaunchArgs([]string{"--ralph", "--limit", "5", "--model", "opus", "--dangerous"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Ralph).To(BeTrue())
		Expect(f.Limit).To(Equal("5"))
		Expect(f.Model).To(Equal("opus"))
		Expect(f.Dangerous).To(BeTrue())
	})

	It("CS-LNCH-001: --dangerously-skip-permissions is an alias of --dangerous", func() {
		f, err := scanLaunchArgs([]string{"--dangerously-skip-permissions"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Dangerous).To(BeTrue())
	})

	It("CS-LNCH-001: --host-access-*-enabled aliases set the same CLI overrides", func() {
		f, err := scanLaunchArgs([]string{
			"--host-access-ssh-enabled", "--host-access-git-enabled",
			"--host-access-docker-socket-enabled", "--host-access-aws-enabled",
		})
		Expect(err).NotTo(HaveOccurred())
		for _, p := range []*bool{f.SSH, f.Git, f.DockerSocket, f.AWS} {
			Expect(p).NotTo(BeNil())
			Expect(*p).To(BeTrue())
		}
	})

	It("CS-LNCH-041, CS-LNCH-043: --worktree forms land in the scanned flag set", func() {
		f, err := scanLaunchArgs([]string{"--worktree"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Worktree).To(HaveValue(BeTrue()))
		Expect(f.WorktreeName).To(BeEmpty())

		f, err = scanLaunchArgs([]string{"--worktree=feature-x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Worktree).To(HaveValue(BeTrue()))
		Expect(f.WorktreeName).To(Equal("feature-x"))

		f, err = scanLaunchArgs([]string{"--no-worktree"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Worktree).To(HaveValue(BeFalse()))

		f, err = scanLaunchArgs(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Worktree).To(BeNil(), "absent means not passed, so env and config decide")
	})

	It("CS-LNCH-043: an invalid --worktree=NAME exits 2", func() {
		for _, bad := range []string{"--worktree=a b", "--worktree=a/b", "--worktree=.git"} {
			_, err := scanLaunchArgs([]string{bad})
			Expect(err).To(HaveOccurred(), bad)
			Expect(execx.ExitCode(err)).To(Equal(2), bad)
		}
	})

	It("CS-LNCH-002: rejects unknown flags with exit code 2", func() {
		_, err := scanLaunchArgs([]string{"--frobnicate"})
		Expect(err).To(HaveOccurred())
		Expect(execx.ExitCode(err)).To(Equal(2))
		Expect(err.Error()).To(ContainSubstring("unknown flag"))
	})

	It("CS-LNCH-002: a known claude flag and all subsequent args pass through", func() {
		for flag := range knownPassthrough {
			if flag == "--model" {
				// --model is owned by the launcher (CS-LNCH-005/023): its value
				// is consumed and re-emitted on the container command instead of
				// starting the passthrough tail.
				continue
			}
			f, err := scanLaunchArgs([]string{flag, "tail", "--frobnicate"})
			Expect(err).NotTo(HaveOccurred(), "flag: %s", flag)
			Expect(f.Passthrough).To(Equal([]string{flag, "tail", "--frobnicate"}), "flag: %s", flag)
		}
	})

	It("CS-LNCH-002: --disallowedTools (Claude Code's real spelling) passes through", func() {
		// Iterating knownPassthrough cannot catch a misspelled entry: the list
		// once held "--disallowTools", so the real flag exited 2 as unknown.
		f, err := scanLaunchArgs([]string{"--disallowedTools", "Bash", "--frobnicate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Passthrough).To(Equal([]string{"--disallowedTools", "Bash", "--frobnicate"}))
		Expect(knownPassthrough).NotTo(HaveKey("--disallowTools"))
	})

	It("CS-LNCH-002: launcher flags before the passthrough boundary are still consumed", func() {
		f, err := scanLaunchArgs([]string{"--dangerous", "--resume", "--rebuild"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Dangerous).To(BeTrue())
		Expect(f.Rebuild).To(BeFalse()) // after --resume: passthrough, not consumed
		Expect(f.Passthrough).To(Equal([]string{"--resume", "--rebuild"}))
	})

	It("CS-LNCH-003: \"--\" ends launcher parsing and passes the rest unmodified", func() {
		f, err := scanLaunchArgs([]string{"--", "--whatever"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Passthrough).To(Equal([]string{"--whatever"}))

		// Even launcher-looking flags after "--" are passthrough.
		f, err = scanLaunchArgs([]string{"--", "--ralph", "--frobnicate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Ralph).To(BeFalse())
		Expect(f.Passthrough).To(Equal([]string{"--ralph", "--frobnicate"}))
	})

	It("CS-LNCH-100: a known claude flag written as --flag=value starts the passthrough", func() {
		for flag := range knownPassthrough {
			if flag == "--model" {
				continue // launcher-owned; its "=" form is covered below
			}
			arg := flag + "=x"
			f, err := scanLaunchArgs([]string{arg, "tail", "--frobnicate"})
			Expect(err).NotTo(HaveOccurred(), "arg: %s", arg)
			Expect(f.Passthrough).To(Equal([]string{arg, "tail", "--frobnicate"}), "arg: %s", arg)
		}

		// The reviewer's case, after launcher flags that are still consumed.
		f, err := scanLaunchArgs([]string{"--dangerous", "--disallowedTools=Bash", "--rebuild"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Dangerous).To(BeTrue())
		Expect(f.Rebuild).To(BeFalse())
		Expect(f.Passthrough).To(Equal([]string{"--disallowedTools=Bash", "--rebuild"}))
	})

	It("CS-LNCH-100: --model=MODEL is consumed by the launcher like --model MODEL", func() {
		f, err := scanLaunchArgs([]string{"--model=opus", "--resume"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Model).To(Equal("opus"))
		Expect(f.Passthrough).To(Equal([]string{"--resume"}))

		_, err = scanLaunchArgs([]string{"--model="})
		Expect(err).To(HaveOccurred())
		Expect(execx.ExitCode(err)).To(Equal(2))
		Expect(err.Error()).To(ContainSubstring("--model requires a value"))
	})

	It("CS-LNCH-100: launcher =value flags keep their launcher meaning", func() {
		f, err := scanLaunchArgs([]string{"--worktree=feature-x", "--attach=otter", "--join=heron"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.WorktreeName).To(Equal("feature-x"))
		Expect(f.AttachTarget).To(Equal("otter"))
		Expect(f.JoinTarget).To(Equal("heron"))
		Expect(f.Passthrough).To(BeNil())
	})

	It("CS-LNCH-100: an unknown flag or a no-value launcher flag with =value still exits 2", func() {
		for _, bad := range []string{"--frobnicate=1", "--dangerous=true", "--rebuild=1", "--=x", "--disallowTools=Bash"} {
			_, err := scanLaunchArgs([]string{bad})
			Expect(err).To(HaveOccurred(), bad)
			Expect(execx.ExitCode(err)).To(Equal(2), bad)
			Expect(err.Error()).To(ContainSubstring("unknown flag"), bad)
		}
	})

	It("CS-LNCH-101: claude's kebab-case aliases of allowlisted flags pass through", func() {
		for _, args := range [][]string{
			{"--allowed-tools", "Bash", "--frobnicate"},
			{"--disallowed-tools", "Bash", "--frobnicate"},
			{"--allowed-tools=Bash", "--frobnicate"},
			{"--disallowed-tools=Bash", "--frobnicate"},
		} {
			f, err := scanLaunchArgs(args)
			Expect(err).NotTo(HaveOccurred(), "args: %v", args)
			Expect(f.Passthrough).To(Equal(args), "args: %v", args)
		}
	})

	It("CS-LNCH-005: --model and --limit require values", func() {
		for _, flag := range []string{"--model", "--limit"} {
			_, err := scanLaunchArgs([]string{flag})
			Expect(err).To(HaveOccurred(), "flag: %s", flag)
			Expect(execx.ExitCode(err)).To(Equal(2), "flag: %s", flag)
		}
	})
})
