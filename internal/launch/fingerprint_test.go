package launch_test

// Spec: spec/sessions.feature (CS-SESS-020..024). The config hash decides
// whether attaching to a running container means attaching to something built
// from a different configuration than what is on disk now.

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("config fingerprint", func() {
	var (
		home, proj, shadow string
		env                map[string]string
		in                 launch.Inputs
	)

	// fresh returns Inputs with a brand-new shadow temp dir, mirroring what a
	// real launch does: the shadow directory is recreated every time, which is
	// exactly why the hash must not depend on its path.
	fresh := func() launch.Inputs {
		dir, err := os.MkdirTemp(shadow, "run")
		Expect(err).NotTo(HaveOccurred())
		cp := in
		cp.TempDir = dir
		return cp
	}

	hashOf := func(i launch.Inputs) string {
		p, err := launch.Build(i)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHash).NotTo(BeEmpty())
		return p.ConfigHash
	}

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		proj = filepath.Join(base, "proj")
		shadow = filepath.Join(base, "shadow")
		mkdir(home)
		mkdir(proj)
		mkdir(shadow)
		env = map[string]string{}
		in = launch.Inputs{
			ProjectDir: proj,
			Home:       home,
			HostUID:    1000,
			HostGID:    1000,
			HostUser:   "tester",
			Getenv:     func(k string) string { return env[k] },
			ImageName:  "claude-sandbox-df-proj-abc123",
			ImageID:    "sha256:aaaa",
			Cfg:        &cascade.Config{},
			Out:        &bytes.Buffer{},
			Err:        &bytes.Buffer{},
		}
	})

	It("CS-SESS-022: an identical relaunch produces an identical hash", func() {
		// The shadow files are rewritten into a fresh temp dir each time. If the
		// hash covered those paths rather than their contents, every launch
		// would look like drift.
		Expect(hashOf(fresh())).To(Equal(hashOf(fresh())))
	})

	It("CS-SESS-020: the hash does not depend on the shadow temp directory path", func() {
		a := fresh()
		b := fresh()
		Expect(a.TempDir).NotTo(Equal(b.TempDir))
		Expect(hashOf(a)).To(Equal(hashOf(b)))
	})

	It("CS-SESS-020: excludes per-session choices (model, passthrough, limit, instance)", func() {
		bare := hashOf(fresh())

		withModel := fresh()
		withModel.CLIModel = "opus"
		Expect(hashOf(withModel)).To(Equal(bare), "model is reported separately, not as drift")

		withPass := fresh()
		withPass.Passthrough = []string{"--resume"}
		Expect(hashOf(withPass)).To(Equal(bare))

		withInstance := fresh()
		withInstance.Instance = "heron"
		Expect(hashOf(withInstance)).To(Equal(bare), "a new session must not look like drift")

		withLimit := fresh()
		withLimit.Limit = "5"
		Expect(hashOf(withLimit)).To(Equal(bare))
	})

	Describe("CS-SESS-023: each input affects the hash", func() {
		var bare string
		BeforeEach(func() { bare = hashOf(fresh()) })

		It("a merged config value changes it", func() {
			i := fresh()
			i.Cfg = &cascade.Config{MemoryLimit: "16g"}
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("an env file's contents change it", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "A=1")
			i := fresh()
			i.EnvFiles = []string{ef}
			first := hashOf(i)
			Expect(first).NotTo(Equal(bare))

			touch(ef, "A=2")
			Expect(hashOf(fresh2(i, shadow))).NotTo(Equal(first))
		})

		It("an added upstream env file changes it", func() {
			a := filepath.Join(proj, "env-a")
			b := filepath.Join(proj, "env-b")
			touch(a, "A=1")
			touch(b, "B=2")
			one := fresh()
			one.EnvFiles = []string{a}
			two := fresh()
			two.EnvFiles = []string{a, b}
			Expect(hashOf(one)).NotTo(Equal(hashOf(two)))
		})

		It("the child image being rebuilt out of band changes it", func() {
			i := fresh()
			i.ImageID = "sha256:bbbb"
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("a different resolved image name changes it", func() {
			i := fresh()
			i.ImageName = "claude-sandbox-df-other-999999"
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("a shadow file's merged contents change it", func() {
			// The host CLAUDE.md is concatenated into the shadow file, so editing
			// it changes what is mounted even though the mount path is identical.
			mkdir(filepath.Join(home, ".claude"))
			touch(filepath.Join(home, ".claude", "CLAUDE.md"), "host memory v1")
			first := hashOf(fresh())
			touch(filepath.Join(home, ".claude", "CLAUDE.md"), "host memory v2")
			Expect(hashOf(fresh())).NotTo(Equal(first))
		})

		It("an added mount changes it", func() {
			i := fresh()
			i.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: "/h", Container: "/c"}}}
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("flipping a mount's read-only flag changes it", func() {
			ro := fresh()
			ro.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: "/h", Container: "/c", Writable: false}}}
			rw := fresh()
			rw.Cfg = &cascade.Config{Mounts: []cascade.Mount{{Host: "/h", Container: "/c", Writable: true}}}
			Expect(hashOf(ro)).NotTo(Equal(hashOf(rw)))
		})

		It("adding a host-access flag changes it", func() {
			i := fresh()
			yes := true
			i.CLISSH = &yes
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("the CLI image being rebuilt (a new cap ID) changes it (CS-SESS-038)", func() {
			// The launch hashes the cap's ID, so a CLI update — which leaves the
			// base and child IDs untouched — still registers as drift.
			i := fresh()
			i.ImageName = "claude-sandbox-df-proj-abc123:run"
			i.ImageID = "sha256:cap-after-cli-update"
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("a different host identity changes it", func() {
			i := fresh()
			i.HostUID = 1001
			Expect(hashOf(i)).NotTo(Equal(bare))
		})

		It("a different memory limit changes it", func() {
			i := fresh()
			i.Cfg = &cascade.Config{MemoryLimit: "2g"}
			Expect(hashOf(i)).NotTo(Equal(bare))
		})
	})

	It("CS-SESS-024: an upstream edit that a local override shadows is not drift", func() {
		// The payoff of hashing the MERGED config rather than the files on disk.
		// Here the upstream value genuinely changes on disk, but a more-local
		// file overrides that same key, so the effective launch is unchanged and
		// the hash must not move. Driven through the real cascade loader so this
		// exercises the actual merge, not a hand-built Config.
		upstream := filepath.Join(proj, "up.yaml")
		local := filepath.Join(proj, "local.yaml")
		touch(upstream, "memoryLimit: 16g\n")
		touch(local, "memoryLimit: 4g\n")

		load := func() *cascade.Config {
			cfg, err := cascade.Load([]string{upstream, local})
			Expect(err).NotTo(HaveOccurred())
			return cfg
		}

		before := load()
		Expect(before.MemoryLimit).To(Equal("4g"), "the local file must win for this test to mean anything")
		i := fresh()
		i.Cfg = before
		first := hashOf(i)

		// Change the shadowed upstream value.
		touch(upstream, "memoryLimit: 32g\n")
		after := load()
		Expect(after.MemoryLimit).To(Equal("4g"))
		j := fresh()
		j.Cfg = after
		Expect(hashOf(j)).To(Equal(first), "a fully shadowed upstream edit is not drift")
	})

	It("CS-SESS-023: an upstream config change that is NOT shadowed is drift", func() {
		// The complement of the case above: same cascade shape, but the changed
		// key has no local override, so the merged result moves and so must the hash.
		upstream := filepath.Join(proj, "up.yaml")
		local := filepath.Join(proj, "local.yaml")
		touch(upstream, "memoryLimit: 16g\n")
		touch(local, "model: opus\n")

		load := func() *cascade.Config {
			cfg, err := cascade.Load([]string{upstream, local})
			Expect(err).NotTo(HaveOccurred())
			return cfg
		}

		i := fresh()
		i.Cfg = load()
		first := hashOf(i)

		touch(upstream, "memoryLimit: 32g\n")
		j := fresh()
		j.Cfg = load()
		Expect(hashOf(j)).NotTo(Equal(first))
	})

	Describe("CS-SESS-021: the inputs label explains what drifted", func() {
		It("records a digest per contributing file, tagged by kind", func() {
			ef := filepath.Join(proj, "env")
			touch(ef, "A=1")
			i := fresh()
			i.EnvFiles = []string{ef}
			p, err := launch.Build(i)
			Expect(err).NotTo(HaveOccurred())

			kinds := map[launch.Kind]int{}
			for _, d := range p.ConfigInputs {
				kinds[d.Kind]++
				Expect(d.Digest).NotTo(BeEmpty())
			}
			Expect(kinds[launch.KindConfig]).To(Equal(1))
			Expect(kinds[launch.KindEnv]).To(Equal(1))
			Expect(kinds[launch.KindImage]).To(Equal(1))
			Expect(kinds[launch.KindShadow]).To(BeNumerically(">", 0))
		})

		It("round-trips through the label encoding", func() {
			p, err := launch.Build(fresh())
			Expect(err).NotTo(HaveOccurred())
			var encoded string
			for _, l := range p.Labels {
				if after, ok := trimPrefix(l, "claude-sandbox.inputs="); ok {
					encoded = after
				}
			}
			Expect(encoded).NotTo(BeEmpty())
			Expect(launch.DecodeInputs(encoded)).To(Equal(p.ConfigInputs))
		})

		It("tolerates a missing or malformed label instead of inventing changes", func() {
			Expect(launch.DecodeInputs("")).To(BeNil())
			Expect(launch.DecodeInputs("   ")).To(BeNil())
			Expect(launch.DecodeInputs("not json")).To(BeNil())
		})
	})

	Describe("Drift", func() {
		mk := func(path, digest string) launch.InputDigest {
			return launch.InputDigest{Path: path, Digest: digest, Kind: launch.KindEnv}
		}

		It("reports nothing when the inputs agree", func() {
			was := []launch.InputDigest{mk("/a", "1"), mk("/b", "2")}
			Expect(launch.Drift(was, was)).To(BeEmpty())
		})

		It("CS-SESS-025: distinguishes changed, added, and removed", func() {
			was := []launch.InputDigest{mk("/same", "1"), mk("/edited", "1"), mk("/gone", "1")}
			now := []launch.InputDigest{mk("/same", "1"), mk("/edited", "2"), mk("/new", "1")}
			got := launch.Drift(was, now)
			Expect(got).To(ConsistOf(
				launch.DriftChange{Path: "/new", How: "added", Kind: launch.KindEnv},
				launch.DriftChange{Path: "/edited", How: "changed", Kind: launch.KindEnv},
				launch.DriftChange{Path: "/gone", How: "removed", Kind: launch.KindEnv},
			))
		})

		It("reports no changes when the recorded inputs are unknown", func() {
			// An older container with no inputs label: everything would look
			// "added", which is noise, so the caller falls back to the bare hash.
			Expect(launch.Drift(nil, nil)).To(BeEmpty())
		})
	})
})

// fresh2 clones i onto a new shadow dir, keeping other fields.
func fresh2(i launch.Inputs, shadowRoot string) launch.Inputs {
	dir, err := os.MkdirTemp(shadowRoot, "run")
	Expect(err).NotTo(HaveOccurred())
	i.TempDir = dir
	return i
}

func trimPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}
