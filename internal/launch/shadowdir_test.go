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

	It("CS-LNCH-082: an unreadable temp root is an error, not a panic", func() {
		_, err := launch.PruneShadowDirs(fake, filepath.Join(root, "missing"), uid, now, launch.ShadowDirMinAge, "")
		Expect(err).To(HaveOccurred())
		Expect(fake.CommandLines()).To(BeEmpty())
	})
})
