package cascade_test

// Spec: spec/config-cascade.feature CS-CASC-046 — each config.yaml is read
// once per launch and every consumer uses that snapshot.

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

var _ = Describe("CS-CASC-046: the config snapshot", func() {
	var tmp string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
	})

	It("CS-CASC-046: every consumer reads the snapshot, not the files rewritten after it", func() {
		files := writeConfigs(tmp,
			"memoryLimit: 16g\ntrackInHost: false\n",
			"oomScoreAdj: 300\ntrackInHost: true\nhostAccess:\n  dockerSocket:\n    enabled: false\n",
		)
		snap, err := cascade.ReadConfigFiles(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cascade.ConfigPaths(snap)).To(Equal(files))

		// A session rewrites both files after the launch read them.
		for _, f := range files {
			Expect(os.WriteFile(f, []byte("hostAccess:\n  dockerSocket:\n    enabled: true\n"), 0o644)).To(Succeed())
		}

		cfg, err := cascade.LoadSnapshot(snap)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.MemoryLimit).To(Equal("16g"))
		Expect(cfg.HostAccess.DockerSocket.Enabled).NotTo(BeNil())
		Expect(*cfg.HostAccess.DockerSocket.Enabled).To(BeFalse())
		Expect(cascade.MemoryLimitSourceOf(snap)).To(Equal(files[0]))
		Expect(cascade.KeySourceOf(snap, "oomScoreAdj")).To(Equal(files[1]))
		Expect(cascade.TrackInHostOf(snap)).To(BeTrue())
		v, set := cascade.TrackInHostExplicitOf(snap)
		Expect(set).To(BeTrue())
		Expect(v).To(BeTrue())
		Expect(cascade.TrackInHostSourceOf(snap)).To(Equal(files[1]))

		// The path-taking forms read again, and see the rewrite: exactly the
		// second read the launch no longer makes.
		Expect(cascade.KeySource(files, "oomScoreAdj")).To(BeEmpty())
	})

	It("CS-CASC-046: a file that cannot be read fails, naming it", func() {
		files := writeConfigs(tmp, "model: opus\n")
		missing := filepath.Join(tmp, "gone", ".claude-sandbox", "config.yaml")
		_, err := cascade.ReadConfigFiles(append(files, missing))
		Expect(err).To(MatchError(ContainSubstring(missing)))
	})

	It("CS-CASC-046: Load is ReadConfigFiles then LoadSnapshot", func() {
		files := writeConfigs(tmp, "memoryLimit: 16g\n", "memoryLimit: 4g\n")
		a, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		snap, err := cascade.ReadConfigFiles(files)
		Expect(err).NotTo(HaveOccurred())
		b, err := cascade.LoadSnapshot(snap)
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(Equal(a))
	})

	It("CS-CASC-046: the cascade report's config.yaml entries come from the snapshot", func() {
		ws := filepath.Join(tmp, "ws")
		proj := filepath.Join(ws, "p")
		wsCfg := filepath.Join(ws, ".claude-sandbox", "config.yaml")
		projCfg := filepath.Join(proj, ".claude-sandbox", "config.yaml")
		for _, f := range []string{wsCfg, projCfg} {
			Expect(os.MkdirAll(filepath.Dir(f), 0o755)).To(Succeed())
			Expect(os.WriteFile(f, []byte("model: opus\n"), 0o644)).To(Succeed())
		}
		snap, err := cascade.ReadConfigFiles([]string{wsCfg})
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		cascade.PrintReportSnapshot(&out, []string{proj, ws}, snap)
		Expect(out.String()).To(ContainSubstring(filepath.Join(ws, ".claude-sandbox") + "/  →  config.yaml"))
		Expect(out.String()).NotTo(ContainSubstring(filepath.Join(proj, ".claude-sandbox")+"/  →  config.yaml"),
			"a config.yaml the launch did not read is not reported as merged")
	})
})
