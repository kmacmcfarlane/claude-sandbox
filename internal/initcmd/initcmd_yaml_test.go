package initcmd_test

// Spec: spec/init.feature CS-INIT-034..036 — init reads trackInHost with the
// launch's own YAML merge (cascade.LoadSnapshot → cfg.TrackInHost), so init
// and the next launch always agree; a config the launch could not parse
// fails init; init rewrites only the top-level trackInHost line.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/initcmd"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// launchReads is what the launch path makes of the cascade: the reference
// init must agree with.
func launchReads(files ...string) (value, isSet bool) {
	snap, err := cascade.ReadConfigFiles(files)
	Expect(err).NotTo(HaveOccurred())
	cfg, err := cascade.LoadSnapshot(snap)
	Expect(err).NotTo(HaveOccurred())
	if cfg.TrackInHost == nil {
		return false, false
	}
	return *cfg.TrackInHost, true
}

var _ = Describe("CS-INIT-034..036: init reads trackInHost as the launch does", func() {
	var ws, wsCfg, proj, sb, cfg string

	BeforeEach(func() {
		os.Unsetenv("CS_GITIGNORE_ASSUME")
		ws = filepath.Join(GinkgoT().TempDir(), "ws")
		wsCfg = filepath.Join(ws, ".claude-sandbox", "config.yaml")
		proj = filepath.Join(ws, "p")
		mkdir(proj)
		sb = filepath.Join(proj, ".claude-sandbox")
		cfg = filepath.Join(sb, "config.yaml")
	})

	// sidecarMode reports whether the layout was set up with trackInHost
	// false: outside a git work tree that mode writes the sidecar .gitignore,
	// the true mode does not (CS-LAY-004).
	sidecarMode := func() bool { return exists(filepath.Join(sb, ".gitignore")) }

	DescribeTable("CS-INIT-034: an upstream spelling the launch reads as a bool is inherited as that bool",
		func(line string, want bool) {
			write(wsCfg, line+"\n")
			lv, lset := launchReads(wsCfg)
			Expect(lset).To(BeTrue())
			Expect(lv).To(Equal(want), "the launch is the reference")

			r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
			Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
			Expect(r.prompter.Asked).To(ConsistOf(ContainSubstring("[Enter=inherit %v, y/n=override]", want)))
			Expect(r.errOut.String()).To(ContainSubstring(wsCfg))
			Expect(read(cfg)).To(ContainSubstring("# trackInHost: %v   # inherited from %s", want, wsCfg))
			Expect(sidecarMode()).To(Equal(!want), "the layout uses the same value")
		},
		Entry("True", "trackInHost: True", true),
		Entry("TRUE", "trackInHost: TRUE", true),
		Entry("yes", "trackInHost: yes", true),
		Entry("on", "trackInHost: on", true),
		Entry("y", "trackInHost: y", true),
		Entry("False", "trackInHost: False", false),
		Entry("no", "trackInHost: no", false),
	)

	It("CS-INIT-034: an upstream null override unsets an upstream true: the greenfield question, not the inherited one", func() {
		write(wsCfg, "trackInHost: true\n")
		mid := filepath.Join(ws, "q", ".claude-sandbox", "config.yaml")
		write(mid, "trackInHost: ~\n")
		proj = filepath.Join(ws, "q", "p")
		mkdir(proj)
		sb = filepath.Join(proj, ".claude-sandbox")
		_, lset := launchReads(wsCfg, mid)
		Expect(lset).To(BeFalse(), "the launch reads the null as unset")

		r := &run{prompter: &prompt.Scripted{IsTTY: true}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).To(ConsistOf(ContainSubstring("Track in host repo?")))
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(sidecarMode()).To(BeTrue(), "greenfield default false")
	})

	It("CS-INIT-034: the project's own empty-valued trackInHost unsets an upstream true for the layout", func() {
		write(wsCfg, "trackInHost: true\n")
		write(cfg, "trackInHost:\n")
		_, lset := launchReads(wsCfg, cfg)
		Expect(lset).To(BeFalse())

		r := &run{}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).To(BeEmpty(), "an existing config asks nothing (CS-INIT-013)")
		Expect(read(cfg)).To(Equal("trackInHost:\n"))
		Expect(sidecarMode()).To(BeTrue(), "unset = false, as the launch reads it")
	})

	It("CS-INIT-034: a nested key of the same name is not trackInHost", func() {
		write(wsCfg, "trackInHost: true\nextra:\n  trackInHost: false\n")
		lv, lset := launchReads(wsCfg)
		Expect(lset).To(BeTrue())
		Expect(lv).To(BeTrue())

		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).To(ConsistOf(ContainSubstring("[Enter=inherit true, y/n=override]")))
		Expect(sidecarMode()).To(BeFalse())
	})

	It("CS-INIT-034: a nested key alone leaves trackInHost unset: the greenfield question", func() {
		write(wsCfg, "extra:\n  trackInHost: true\n")
		_, lset := launchReads(wsCfg)
		Expect(lset).To(BeFalse())

		r := &run{prompter: &prompt.Scripted{IsTTY: true}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(r.prompter.Asked).NotTo(ContainElement(ContainSubstring("Enter=inherit")))
		Expect(sidecarMode()).To(BeTrue())
	})

	It("CS-INIT-034: the source is the most-local file whose value the merge kept", func() {
		write(wsCfg, "trackInHost: false\n")
		mid := filepath.Join(ws, "q", ".claude-sandbox", "config.yaml")
		write(mid, "trackInHost: Yes\n")
		proj = filepath.Join(ws, "q", "p")
		mkdir(proj)
		cfg = filepath.Join(proj, ".claude-sandbox", "config.yaml")

		r := &run{prompter: &prompt.Scripted{IsTTY: true, Answers: []string{""}}}
		Expect(r.init(proj, initcmd.Flags{})).To(Succeed())
		Expect(read(cfg)).To(ContainSubstring("# trackInHost: true   # inherited from " + mid))
	})

	DescribeTable("CS-INIT-035: an upstream config the launch could not parse fails init, naming it",
		func(content string) {
			write(wsCfg, content)
			_, err := cascade.LoadSnapshot([]cascade.ConfigFile{{Path: wsCfg, Content: []byte(content)}})
			Expect(err).To(HaveOccurred(), "the launch fails on it too")

			err = (&run{}).init(proj, initcmd.Flags{TrackInHost: ptr(false)})
			Expect(err).To(MatchError(ContainSubstring(wsCfg)))
			Expect(exists(cfg)).To(BeFalse(), "nothing seeded")
		},
		Entry("not YAML", "trackInHost: [\n"),
		Entry("a quoted string", "trackInHost: \"true\"\n"),
		Entry("a number", "trackInHost: 1\n"),
	)

	It("CS-INIT-035: the project's own config read for the layout value fails init when it does not parse", func() {
		write(cfg, "trackInHost: [\n")
		err := (&run{}).init(proj, initcmd.Flags{})
		Expect(err).To(MatchError(ContainSubstring(cfg)))
	})

	It("CS-INIT-036: --track-in-host on an existing config rewrites only the top-level line", func() {
		write(cfg, "extra:\n  trackInHost: false\n# trackInHost: false\n")
		Expect((&run{}).init(proj, initcmd.Flags{TrackInHost: ptr(true)})).To(Succeed())
		Expect(read(cfg)).To(Equal("extra:\n  trackInHost: false\ntrackInHost: true\n"))
		lv, lset := launchReads(cfg)
		Expect(lset).To(BeTrue())
		Expect(lv).To(BeTrue())
	})

	It("CS-INIT-036: with only a nested key, the top-level key is appended", func() {
		write(cfg, "extra:\n  trackInHost: false\n")
		Expect(initcmd.SetTrackInHost(cfg, true)).To(Succeed())
		Expect(read(cfg)).To(Equal("extra:\n  trackInHost: false\n\ntrackInHost: true\n"))
		lv, lset := launchReads(cfg)
		Expect(lset).To(BeTrue())
		Expect(lv).To(BeTrue())
	})

	It("CS-INIT-036: the inherited hint never rewrites a nested key", func() {
		write(cfg, "extra:\n  trackInHost: false\n# trackInHost: false\n")
		Expect(initcmd.SetTrackInHostHint(cfg, true, wsCfg)).To(Succeed())
		Expect(read(cfg)).To(Equal("extra:\n  trackInHost: false\n# trackInHost: true   # inherited from " + wsCfg + "\n"))
	})
})
