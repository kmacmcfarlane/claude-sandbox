package cascade_test

// Spec: spec/config-cascade.feature (CS-CASC).
//
// CS-CASC-010 (env files stack as --env-file flags in root-first order) is
// launcher argument assembly — covered by the launch suite, intentionally
// skipped here. CS-CASC-011 and CS-CASC-012 are launcher scenarios tested here
// at the package level: Config.Validate and Load respectively.

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

// writeConfigs writes each content as <tmp>/lvl<N>/.claude-sandbox/config.yaml
// and returns the file paths in the given (root-first) order.
func writeConfigs(tmp string, contents ...string) []string {
	files := make([]string, 0, len(contents))
	for i, c := range contents {
		dir := filepath.Join(tmp, "lvl"+string(rune('0'+i)), ".claude-sandbox")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		f := filepath.Join(dir, "config.yaml")
		Expect(os.WriteFile(f, []byte(c), 0o644)).To(Succeed())
		files = append(files, f)
	}
	return files
}

var _ = Describe("config cascade", func() {
	var tmp string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
	})

	It("CS-CASC-001: more-local scalar wins", func() {
		files := writeConfigs(tmp,
			"memoryLimit: 16g\n",
			"memoryLimit: 4g\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.MemoryLimit).To(Equal("4g"))
	})

	It("CS-CASC-002: upstream keys survive when the local file is sparse", func() {
		files := writeConfigs(tmp,
			"model: opus\n",
			"# everything commented\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model).To(Equal("opus"))
	})

	It("CS-CASC-003: maps deep-merge key-by-key", func() {
		files := writeConfigs(tmp,
			"hostAccess:\n  ssh:\n    enabled: true\n  git:\n    enabled: true\n",
			"hostAccess:\n  git:\n    enabled: false\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.HostAccess.SSH.Enabled).To(HaveValue(BeTrue()))
		Expect(cfg.HostAccess.Git.Enabled).To(HaveValue(BeFalse()))
	})

	It("CS-CASC-004: mounts append across levels", func() {
		files := writeConfigs(tmp,
			"mounts:\n  - host: /data/a\n    container: /mnt/a\n    writable: false\n",
			"mounts:\n  - host: /data/b\n    container: /mnt/b\n    writable: true\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Mounts).To(Equal([]cascade.Mount{
			{Host: "/data/a", Container: "/mnt/a", Writable: false},
			{Host: "/data/b", Container: "/mnt/b", Writable: true},
		}))
	})

	It("CS-CASC-005: a same host+container mount overrides the upstream entry", func() {
		files := writeConfigs(tmp,
			"mounts:\n  - host: /data/a\n    container: /mnt/a\n    writable: false\n",
			"mounts:\n  - host: /data/a\n    container: /mnt/a\n    writable: true\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Mounts).To(Equal([]cascade.Mount{
			{Host: "/data/a", Container: "/mnt/a", Writable: true},
		}))
	})

	It("CS-CASC-006: trackInHost: most-local explicit setting wins", func() {
		files := writeConfigs(tmp,
			"trackInHost: true\n",
			"trackInHost: false\n",
		)
		Expect(cascade.TrackInHost(files)).To(BeFalse())
	})

	It("CS-CASC-007: trackInHost defaults to false when never set", func() {
		files := writeConfigs(tmp,
			"model: opus\n",
			"memoryLimit: 4g\n",
		)
		Expect(cascade.TrackInHost(files)).To(BeFalse())

		_, isSet := cascade.TrackInHostExplicit(files)
		Expect(isSet).To(BeFalse())
	})

	It("CS-CASC-008: commented trackInHost lines are ignored", func() {
		files := writeConfigs(tmp, "# trackInHost: true\n")
		Expect(cascade.TrackInHost(files)).To(BeFalse())

		_, isSet := cascade.TrackInHostExplicit(files)
		Expect(isSet).To(BeFalse())
	})

	It("CS-CASC-009: a sparse local file does not mask an upstream trackInHost", func() {
		files := writeConfigs(tmp,
			"trackInHost: true\n",
			"# trackInHost: false\n",
		)
		Expect(cascade.TrackInHost(files)).To(BeTrue())
		Expect(cascade.TrackInHostSource(files)).To(Equal(files[0]))
	})

	// CS-CASC-010 is launcher --env-file assembly; covered by the launch suite.

	It("CS-CASC-011: mount entries must define host and container", func() {
		files := writeConfigs(tmp,
			"mounts:\n  - host: /data/a\n",
		)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())

		err = cfg.Validate(files)
		Expect(err).To(MatchError(ContainSubstring("mounts[0]")))
		Expect(err).To(MatchError(ContainSubstring(files[0])))

		// A complete mount validates cleanly.
		ok := &cascade.Config{Mounts: []cascade.Mount{{Host: "/a", Container: "/b"}}}
		Expect(ok.Validate(files)).To(Succeed())
	})

	It("CS-CASC-012: recognized top-level keys", func() {
		files := writeConfigs(tmp, `model: sonnet
memoryLimit: 8g
disableUpdateCheck: true
trackInHost: true
baseOnly: true
dockerfileDir: /somewhere
dockerfile: /somewhere/Dockerfile.alt
hostAccess:
  ssh:
    enabled: true
  git:
    enabled: false
  dockerSocket:
    enabled: true
  aws:
    enabled: false
mounts:
  - host: /data/a
    container: /mnt/a
    writable: true
`)
		cfg, err := cascade.Load(files)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model).To(Equal("sonnet"))
		Expect(cfg.MemoryLimit).To(Equal("8g"))
		Expect(cfg.DisableUpdateCheck).To(BeTrue())
		Expect(cfg.TrackInHost).To(HaveValue(BeTrue()))
		Expect(cfg.BaseOnly).To(BeTrue())
		Expect(cfg.DockerfileDir).To(Equal("/somewhere"))
		Expect(cfg.Dockerfile).To(Equal("/somewhere/Dockerfile.alt"))
		Expect(cfg.HostAccess.SSH.Enabled).To(HaveValue(BeTrue()))
		Expect(cfg.HostAccess.Git.Enabled).To(HaveValue(BeFalse()))
		Expect(cfg.HostAccess.DockerSocket.Enabled).To(HaveValue(BeTrue()))
		Expect(cfg.HostAccess.AWS.Enabled).To(HaveValue(BeFalse()))
		Expect(cfg.Mounts).To(Equal([]cascade.Mount{
			{Host: "/data/a", Container: "/mnt/a", Writable: true},
		}))
	})
})
