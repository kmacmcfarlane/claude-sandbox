package launch_test

// Spec: spec/launch.feature (CS-LNCH-080..082) — the shadow directory is named
// on the container, and a launch sweeps the directories no container uses.
// Every test works in its own scratch temp root; the real one is never read.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("shadow directory lifecycle (CS-LNCH-080..082)", func() {
	var (
		root string
		fake *execx.Fake
		now  time.Time
		uid  int
	)
	const psPattern = "docker ps -a --no-trunc"

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		root = filepath.Join(base, "tmp")
		mkdir(root)
		fake = &execx.Fake{}
		now = time.Now()
		uid = os.Getuid()
	})

	// shadow makes a directory under root holding one file, last modified age
	// ago (the file is written first: writing it bumps the directory's mtime).
	shadow := func(name string, age time.Duration) string {
		d := filepath.Join(root, name)
		mkdir(d)
		touch(filepath.Join(d, "CLAUDE.md"), "x")
		t := now.Add(-age)
		Expect(os.Chtimes(d, t, t)).To(Succeed())
		return d
	}
	prune := func(keep string) ([]string, error) {
		return launch.PruneShadowDirs(fake, root, uid, now, launch.ShadowDirMinAge, keep)
	}
	psCalls := func() int {
		n := 0
		for _, l := range fake.CommandLines() {
			if strings.HasPrefix(l, psPattern) {
				n++
			}
		}
		return n
	}

	It("CS-LNCH-080: the container is labelled with its shadow directory, outside the config hash", func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home, proj := filepath.Join(base, "home"), filepath.Join(base, "proj")
		mkdir(home)
		mkdir(proj)
		in := launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(string) string { return "" },
			ImageName: "img", Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		}
		a, b := in, in
		a.TempDir, b.TempDir = filepath.Join(base, "claude-sandbox1"), filepath.Join(base, "claude-sandbox2")
		mkdir(a.TempDir)
		mkdir(b.TempDir)
		pa, err := launch.Build(a)
		Expect(err).NotTo(HaveOccurred())
		pb, err := launch.Build(b)
		Expect(err).NotTo(HaveOccurred())

		Expect(pa.ShadowDir).To(Equal(a.TempDir))
		Expect(pa.Labels).To(ContainElement(launch.LabelShadowDir + "=" + a.TempDir))
		Expect(pa.CreateArgs(proj)).To(ContainElements("--label", "claude-sandbox.shadowdir="+a.TempDir))
		Expect(pa.ConfigHash).To(Equal(pb.ConfigHash), "a different shadow directory is not drift")
	})

	It("CS-LNCH-081: removes an old, owned, unreferenced shadow directory and keeps everything else", func() {
		old := shadow("claude-sandbox111", 2*time.Hour)
		young := shadow("claude-sandbox222", 10*time.Minute)
		labelled := shadow("claude-sandbox333", 2*time.Hour)
		mounted := shadow("claude-sandbox444", 2*time.Hour) // a pre-label launcher's container
		own := shadow("claude-sandbox555", 2*time.Hour)
		lookalike := shadow("claude-sandbox-666", 2*time.Hour)
		other := shadow("otherthing777", 2*time.Hour)

		// A symlink named like a shadow directory, pointing at an old
		// directory elsewhere: neither the link nor its target is touched.
		target := filepath.Join(filepath.Dir(root), "target")
		mkdir(target)
		touch(filepath.Join(target, "keep"), "x")
		link := filepath.Join(root, "claude-sandbox888")
		Expect(os.Symlink(target, link)).To(Succeed())
		t := now.Add(-2 * time.Hour)
		Expect(os.Chtimes(target, t, t)).To(Succeed())
		// A plain file named like one.
		file := filepath.Join(root, "claude-sandbox999")
		touch(file, "x")
		Expect(os.Chtimes(file, t, t)).To(Succeed())

		// One running labelled container, and one exited container with no
		// shadowdir label whose mounts name a file in another directory. The
		// other container's temp root differs (a launch from another sandbox):
		// the base name is what matches.
		fake.On(psPattern, strings.Join([]string{
			labelled + "\x1f" + "/home/u/proj," + labelled + "/CLAUDE.md",
			"\x1f" + "/elsewhere/tmp/claude-sandbox444/.mcp.json,/home/u/.claude",
			"\x1f",
		}, "\n")+"\n", nil)

		removed, err := prune(own)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(Equal([]string{old}))
		Expect(old).NotTo(BeADirectory())
		for _, d := range []string{young, labelled, mounted, own, lookalike, other, target} {
			Expect(d).To(BeADirectory())
		}
		Expect(filepath.Join(target, "keep")).To(BeARegularFile())
		_, err = os.Lstat(link)
		Expect(err).NotTo(HaveOccurred(), "the symlink itself is kept")
		Expect(file).To(BeARegularFile())
		Expect(psCalls()).To(Equal(1))
		Expect(fake.CommandLines()[0]).NotTo(ContainSubstring("--filter"), "every container, any state, any labels")
	})

	It("CS-LNCH-081: a directory owned by someone else is never removed", func() {
		shadow("claude-sandbox111", 2*time.Hour)
		fake.On(psPattern, "", nil)
		removed, err := launch.PruneShadowDirs(fake, root, uid+1, now, launch.ShadowDirMinAge, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeEmpty())
		Expect(filepath.Join(root, "claude-sandbox111")).To(BeADirectory())
		Expect(psCalls()).To(Equal(0), "not a candidate, so no listing either")
	})

	It("CS-LNCH-082: no candidate means no docker call", func() {
		shadow("claude-sandbox111", time.Minute)
		shadow("unrelated", 5*time.Hour)
		removed, err := prune("")
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeEmpty())
		Expect(fake.CommandLines()).To(BeEmpty())
	})

	It("CS-LNCH-082: a failed listing removes nothing and reports an error", func() {
		old := shadow("claude-sandbox111", 2*time.Hour)
		fake.On(psPattern, "", execx.Fail(1))
		removed, err := prune("")
		Expect(err).To(HaveOccurred())
		Expect(removed).To(BeEmpty())
		Expect(old).To(BeADirectory())
	})

	// unremovable makes an old shadow directory holding a read-only
	// subdirectory with a file in it, so RemoveAll fails part-way. The
	// subdirectory is made writable again at cleanup (wherever the directory
	// ended up), or the test temp dir could not be removed either.
	unremovable := func(name string) string {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		d := filepath.Join(root, name)
		sub := filepath.Join(d, "locked")
		mkdir(sub)
		touch(filepath.Join(sub, "f"), "x")
		Expect(os.Chmod(sub, 0o555)).To(Succeed())
		DeferCleanup(func() {
			for _, p := range []string{sub, filepath.Join(d+launch.UnremovableSuffix, "locked")} {
				_ = os.Chmod(p, 0o755)
			}
		})
		t := now.Add(-2 * time.Hour)
		Expect(os.Chtimes(d, t, t)).To(Succeed())
		return d
	}

	It("CS-LNCH-082: a removal that fails part-way is reported, the other candidates are still removed, and the directory is set aside", func() {
		stuck := unremovable("claude-sandbox111")
		old := shadow("claude-sandbox222", 2*time.Hour)
		fake.On(psPattern, "", nil)

		removed, err := prune("")
		Expect(removed).To(Equal([]string{old}))
		Expect(old).NotTo(BeADirectory())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("permission denied"))
		Expect(err.Error()).To(ContainSubstring(stuck + launch.UnremovableSuffix))
		Expect(err.Error()).To(ContainSubstring("remove it by hand"))
		Expect(stuck).NotTo(BeADirectory())
		Expect(filepath.Join(stuck+launch.UnremovableSuffix, "locked", "f")).To(BeARegularFile())

		// A later launch has no candidate: no listing, no error, no warning.
		removed, err = prune("")
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeEmpty())
		Expect(psCalls()).To(Equal(1))
	})

	It("CS-LNCH-082: when it cannot be renamed either, the directory is backed off for an hour", func() {
		stuck := unremovable("claude-sandbox111")
		// A non-empty directory already holds the aside name: rename fails.
		blocker := filepath.Join(root, "claude-sandbox111"+launch.UnremovableSuffix)
		mkdir(blocker)
		touch(filepath.Join(blocker, "x"), "x")
		fake.On(psPattern, "", nil)

		_, err := prune("")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("permission denied"))
		Expect(err.Error()).To(ContainSubstring("retrying in 1h"))
		Expect(stuck).To(BeADirectory())

		_, err = prune("")
		Expect(err).NotTo(HaveOccurred(), "skipped while young")
		Expect(psCalls()).To(Equal(1))

		now = now.Add(launch.ShadowDirMinAge + time.Minute)
		_, err = prune("")
		Expect(err).To(HaveOccurred(), "retried once the hour is up")
		Expect(psCalls()).To(Equal(2))
	})

	It("CS-LNCH-082: an unreadable temp root is an error, not a panic", func() {
		_, err := launch.PruneShadowDirs(fake, filepath.Join(root, "missing"), uid, now, launch.ShadowDirMinAge, "")
		Expect(err).To(HaveOccurred())
		Expect(fake.CommandLines()).To(BeEmpty())
	})
})

// Not a production behaviour: under go test the real temp root is refused
// outright, so a fixture that forgets its scratch root fails loudly instead of
// sweeping the shadow directories of live sessions on the test machine.
var _ = Describe("test guard: the real temp root is refused under go test (CS-LNCH-082)", func() {
	const guard = "real temp root"

	It("CS-LNCH-082: NewShadowDir with the default root panics and makes nothing", func() {
		Expect(func() { _, _ = launch.NewShadowDir("") }).To(PanicWith(ContainSubstring(guard)))
		Expect(func() { _, _ = launch.NewShadowDir(os.TempDir()) }).To(PanicWith(ContainSubstring(guard)))
	})

	It("CS-LNCH-082: PruneShadowDirs with the default root, however spelled, panics before listing anything", func() {
		fake := &execx.Fake{}
		for _, root := range []string{"", os.TempDir(), os.TempDir() + "/", filepath.Join(os.TempDir(), ".")} {
			Expect(func() {
				_, _ = launch.PruneShadowDirs(fake, root, os.Getuid(), time.Now(), launch.ShadowDirMinAge, "")
			}).To(PanicWith(ContainSubstring(guard)), "root %q", root)
		}
		Expect(fake.CommandLines()).To(BeEmpty())
	})

	It("CS-LNCH-082: a symlink to the real temp root is refused too", func() {
		link := filepath.Join(GinkgoT().TempDir(), "tmp-link")
		Expect(os.Symlink(os.TempDir(), link)).To(Succeed())
		Expect(func() { _, _ = launch.NewShadowDir(link) }).To(PanicWith(ContainSubstring(guard)))
	})

	It("CS-LNCH-082: a scratch root under the temp root is allowed", func() {
		d, err := launch.NewShadowDir(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(BeADirectory())
	})
})

// CS-LNCH-161/162: a launcher inside a sandbox must make its shadow directory
// where the host daemon resolves the same path. Every path here is under a
// scratch directory; the real temp root is never named.
var _ = Describe("nested shadow root (CS-LNCH-161/162)", func() {
	var (
		home string
		env  map[string]string
	)
	getenv := func(k string) string { return env[k] }
	var mountText string
	mounts := func() (string, error) { return mountText, nil }

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(base, "home")
		mkdir(filepath.Join(home, ".claude", "tmp"))
		mountText = ""
		env = map[string]string{
			"CLAUDE_SANDBOX_PROJECT_DIR": filepath.Join(base, "proj"),
			"CLAUDE_CODE_TMPDIR":         filepath.Join(home, ".claude", "tmp"),
		}
	})

	It("CS-LNCH-161: outside a sandbox the root is the temp root, unchanged", func() {
		delete(env, "CLAUDE_SANDBOX_PROJECT_DIR")
		root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(BeEmpty())
		Expect(filepath.Join(home, ".claude", "tmp", launch.NestedShadowSubdir)).NotTo(BeADirectory())
	})

	It("CS-LNCH-161: inside, the root is a 0700 directory the user owns under CLAUDE_CODE_TMPDIR", func() {
		root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(Equal(filepath.Join(home, ".claude", "tmp", "claude-sandbox-shadow")))
		fi, err := os.Lstat(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.IsDir()).To(BeTrue())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o700)))

		d, err := launch.NewShadowDir(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Dir(d)).To(Equal(root))
		Expect(filepath.Base(root)).NotTo(MatchRegexp(`^claude-sandbox[0-9]+$`), "the subdir itself is never a sweep candidate")
	})

	It("CS-LNCH-161: CLAUDE_CONFIG_DIR is the config dir when set", func() {
		cfg := filepath.Join(home, "alt-config")
		env["CLAUDE_CONFIG_DIR"] = cfg
		env["CLAUDE_CODE_TMPDIR"] = filepath.Join(cfg, "tmp") + "/"
		root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(Equal(filepath.Join(cfg, "tmp", "claude-sandbox-shadow")))

		env["CLAUDE_CODE_TMPDIR"] = filepath.Join(home, ".claude", "tmp")
		_, err = launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).To(MatchError(launch.ErrNoHostVisibleTempRoot), "~/.claude is not the config dir then")
	})

	It("CS-LNCH-161: a non-empty TMPDIR is the root, unchecked and not created", func() {
		env["TMPDIR"] = filepath.Join(home, "chosen")
		root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(Equal(filepath.Join(home, "chosen")))
		Expect(root).NotTo(BeADirectory())
	})

	DescribeTable("CS-LNCH-162: no host-visible root is an error naming why, and nothing is created",
		func(cct, want string) {
			if cct == "<unset>" {
				delete(env, "CLAUDE_CODE_TMPDIR")
			} else {
				env["CLAUDE_CODE_TMPDIR"] = strings.ReplaceAll(cct, "$HOME", home)
			}
			root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
			Expect(err).To(MatchError(launch.ErrNoHostVisibleTempRoot))
			Expect(err.Error()).To(ContainSubstring(want))
			Expect(root).To(BeEmpty())
			Expect(filepath.Join(home, ".claudex", "tmp", launch.NestedShadowSubdir)).NotTo(BeADirectory())
		},
		Entry("unset", "<unset>", "CLAUDE_CODE_TMPDIR is not set"),
		Entry("relative", ".claude/tmp", "not an absolute path"),
		Entry("outside the config dir", "$HOME/elsewhere", "not under the config dir"),
		Entry("a sibling sharing the prefix", "$HOME/.claudex/tmp", "not under the config dir"),
	)

	It("CS-LNCH-162: a root that cannot be made the user's directory is an error", func() {
		touch(filepath.Join(home, ".claude", "tmp", launch.NestedShadowSubdir), "a file in the way")
		_, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).To(MatchError(launch.ErrNoHostVisibleTempRoot))
		Expect(err.Error()).To(ContainSubstring("cannot prepare"))
	})

	It("CS-LNCH-161: a config dir reached through a symlink still counts", func() {
		real := filepath.Join(home, "real-config")
		mkdir(filepath.Join(real, "tmp"))
		link := filepath.Join(home, "link-config")
		Expect(os.Symlink(real, link)).To(Succeed())
		env["CLAUDE_CONFIG_DIR"] = link
		env["CLAUDE_CODE_TMPDIR"] = filepath.Join(real, "tmp")
		root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(Equal(filepath.Join(real, "tmp", launch.NestedShadowSubdir)))
		Expect(root).To(BeADirectory())

		env["CLAUDE_CONFIG_DIR"] = real
		env["CLAUDE_CODE_TMPDIR"] = filepath.Join(link, "tmp")
		_, err = launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).NotTo(HaveOccurred(), "the variable spelled through the link")
	})

	It("CS-LNCH-162: a relative TMPDIR is refused", func() {
		env["TMPDIR"] = "scratch"
		_, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
		Expect(err).To(MatchError(launch.ErrNoHostVisibleTempRoot))
		Expect(err.Error()).To(ContainSubstring("not an absolute path"))
	})

	Describe("CS-LNCH-162: an explicit TMPDIR on a definitely container-local mount", func() {
		// proc(5) lines: id parent maj:min root mountpoint opts [optional...] - fstype source superopts
		line := func(mp, fstype string) string {
			return "1 0 0:1 / " + mp + " rw,relatime shared:1 - " + fstype + " src rw\n"
		}
		BeforeEach(func() {
			mountText = line("/", "overlay") +
				line(home, "ext4") +
				line(filepath.Join(home, "ram"), "tmpfs") +
				line(strings.ReplaceAll(filepath.Join(home, "with space"), " ", `\040`), "tmpfs")
		})
		DescribeTable("is refused on the root filesystem or a tmpfs, trusted elsewhere",
			func(tmpdir string, refused bool, want string) {
				env["TMPDIR"] = strings.ReplaceAll(tmpdir, "$HOME", home)
				root, err := launch.NestedShadowRoot(getenv, home, nil, mounts)
				if refused {
					Expect(err).To(MatchError(launch.ErrNoHostVisibleTempRoot))
					Expect(err.Error()).To(ContainSubstring(want))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(root).To(Equal(env["TMPDIR"]))
			},
			Entry("the container's /tmp", "/tmp/somewhere-75d8", true, "the container's own root filesystem"),
			Entry("a tmpfs", "$HOME/ram/x", true, "a tmpfs mounted at"),
			Entry("a bind mount", "$HOME/scratch", false, ""),
			Entry("a tmpfs whose mount point has an escaped space (decoded)", "$HOME/with space/x", true, "with space"),
			Entry("a sibling sharing a mount point's prefix is the parent's mount", "$HOME/ramx", false, ""),
		)
		It("an unreadable mountinfo is no evidence: TMPDIR is trusted", func() {
			env["TMPDIR"] = "/tmp/somewhere-75d8"
			root, err := launch.NestedShadowRoot(getenv, home, nil, func() (string, error) { return "", os.ErrPermission })
			Expect(err).NotTo(HaveOccurred())
			Expect(root).To(Equal("/tmp/somewhere-75d8"))
		})
	})
})
