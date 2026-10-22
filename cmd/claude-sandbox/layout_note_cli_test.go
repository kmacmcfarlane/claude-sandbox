package main

// Spec: spec/layout.feature CS-LAY-026 — an ordinary launch with every
// CS-LAY-009 entry already present has nothing to decide: no note, no child
// probe. (init prints the note regardless; internal/initcmd pins that side.)

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

var _ = Describe("launch layout note (CS-LAY-026)", func() {
	It("CS-LAY-026: a launch with every entry present prints no note and asks no child probe", func() {
		f := newCLIFixture()
		sb := filepath.Join(f.proj, ".claude-sandbox")
		Expect(os.MkdirAll(sb, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sb, "config.yaml"), []byte("trackInHost: true\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(f.proj, ".gitignore"), []byte(
			".claude-sandbox/*\n.claude-sandbox/env\n.claude-sandbox/temp/\n.claude-sandbox/ralph/\n"+
				"!.claude-sandbox/config.yaml\n!.claude-sandbox/Dockerfile\n.claude/worktrees/\n"), 0o644)).To(Succeed())
		f.fake.On("--no-index", "", execx.Fail(1)) // the directory itself is not excluded
		f.fake.On("check-ignore", "", nil)         // every child probe would be ignored

		Expect(f.run()).To(Equal(0), f.errw.String())

		Expect(f.errw.String()).NotTo(ContainSubstring("hides new files under .claude-sandbox/"))
		Expect(f.errw.String()).NotTo(ContainSubstring("WARNING: trackInHost"))
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(ContainSubstring("ignore-probe"), "no child probe on a launch with nothing to decide")
		}
		Expect(strings.Join(f.fake.CommandLines(), "\n")).To(ContainSubstring("check-ignore"), "the layout step did run (directory probe)")
	})
})
