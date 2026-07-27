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

	It("CS-LNCH-005: --model and --limit require values", func() {
		for _, flag := range []string{"--model", "--limit"} {
			_, err := scanLaunchArgs([]string{flag})
			Expect(err).To(HaveOccurred(), "flag: %s", flag)
			Expect(execx.ExitCode(err)).To(Equal(2), "flag: %s", flag)
		}
	})
})
