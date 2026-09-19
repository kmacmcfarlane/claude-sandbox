package main

// Spec: spec/launch.feature (CS-LNCH-080..084) — the launch's shadow directory
// is made under the lock and labelled on the container, a launch sweeps the
// directories no container uses, a failed launch removes its own, and the
// drift check leaves none. The fixture's Env.TempRoot is a scratch directory,
// so nothing here reads or sweeps the real temp root.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// countingLock wraps the fixture lock and records how many entries the temp
// root held at the moment the lock was taken.
type countingLock struct {
	*fakeLock
	root string
	seen []int
}

func (l *countingLock) Acquire() (func(), error) {
	entries, _ := os.ReadDir(l.root)
	l.seen = append(l.seen, len(entries))
	return l.fakeLock.Acquire()
}

// labelValue returns the value of the --label KEY=... flag on a create argv.
func labelValue(args []string, key string) string {
	for i, a := range args {
		if a == "--label" && i+1 < len(args) && strings.HasPrefix(args[i+1], key+"=") {
			return strings.TrimPrefix(args[i+1], key+"=")
		}
	}
	return ""
}

func dirNames(root string) []string {
	entries, err := os.ReadDir(root)
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// oldShadow makes an old shadow directory under the fixture's temp root.
func oldShadow(f *cliFixture, name string) string {
	d := filepath.Join(f.tmp, name)
	Expect(os.MkdirAll(d, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(d, "CLAUDE.md"), []byte("x"), 0o644)).To(Succeed())
	t := time.Now().Add(-2 * time.Hour)
	Expect(os.Chtimes(d, t, t)).To(Succeed())
	return d
}

const sweepPS = "docker ps -a --no-trunc"

var _ = Describe("shadow directory lifecycle (CS-LNCH-080..084)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	It("CS-LNCH-080: the directory is made after the lock is taken and named on the container", func() {
		lock := &countingLock{fakeLock: f.lock, root: f.tmp}
		f.env.Lock = lock
		Expect(f.run()).To(Equal(0), f.errw.String())

		Expect(lock.seen).To(Equal([]int{0}), "no shadow directory existed before the lock was taken")
		dir := labelValue(f.launched().Args, launch.LabelShadowDir)
		Expect(filepath.Dir(dir)).To(Equal(f.tmp))
		Expect(filepath.Base(dir)).To(MatchRegexp(`^claude-sandbox[0-9]+$`))
		Expect(filepath.Join(dir, "CLAUDE.md")).To(BeARegularFile(), "kept: the container mounts it")
		Expect(f.launchLine()).To(ContainSubstring(dir + "/CLAUDE.md:"))
	})

	It("CS-LNCH-081: an old unreferenced directory is swept under the lock, after discovery", func() {
		stale := oldShadow(f, "claude-sandbox12345")
		inUse := oldShadow(f, "claude-sandbox67890")
		f.fake.On(sweepPS, "\x1f"+inUse+"/CLAUDE.md\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())

		Expect(stale).NotTo(BeADirectory())
		Expect(inUse).To(BeADirectory())
		held := f.lock.held()
		Expect(held[0]).To(HavePrefix("docker ps -a --filter "), "discovery first")
		Expect(held).To(ContainElement(HavePrefix(sweepPS)), "the sweep's listing runs under the lock")
		Expect(f.errw.String()).NotTo(ContainSubstring("shadow"), "a successful sweep prints nothing")
	})

	It("CS-LNCH-082: a failed listing warns once, removes nothing, and the launch proceeds", func() {
		stale := oldShadow(f, "claude-sandbox12345")
		f.fake.On(sweepPS, "", execx.Fail(1))
		Expect(f.run()).To(Equal(0), f.errw.String())

		Expect(stale).To(BeADirectory())
		Expect(strings.Count(f.errw.String(), "could not clean up old shadow directories")).To(Equal(1))
		Expect(f.fake.Execed).NotTo(BeNil(), "the session still starts")
	})

	It("CS-LNCH-082: a directory that cannot be removed warns once, and the next launch is silent", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		stuck := oldShadow(f, "claude-sandbox12345")
		sub := filepath.Join(stuck, "locked")
		Expect(os.Mkdir(sub, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644)).To(Succeed())
		Expect(os.Chmod(sub, 0o555)).To(Succeed())
		aside := stuck + launch.UnremovableSuffix
		DeferCleanup(func() { _ = os.Chmod(filepath.Join(aside, "locked"), 0o755) })
		t := time.Now().Add(-2 * time.Hour)
		Expect(os.Chtimes(stuck, t, t)).To(Succeed())
		f.fake.On(sweepPS, "", nil)

		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(strings.Count(f.errw.String(), "could not clean up old shadow directories")).To(Equal(1))
		Expect(f.errw.String()).To(ContainSubstring(aside))
		Expect(f.fake.Execed).NotTo(BeNil(), "the session still starts")

		f.errw.Reset()
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("shadow"), "set aside: never warned about again")
	})

	It("CS-LNCH-082: without a candidate there is no sweep listing", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix(sweepPS))
		}
	})

	It("CS-LNCH-083: a failed create removes the launch's own directory", func() {
		f.fake.OnFunc("docker create", func(c execx.Cmd) (string, error) {
			c.Stderr.Write([]byte("docker: Error response from daemon: invalid mount config"))
			return "", execx.Fail(125)
		})
		Expect(f.run()).NotTo(Equal(0))
		Expect(dirNames(f.tmp)).To(BeEmpty())
	})

	It("CS-LNCH-083: a failed start removes the reservation, then the directory", func() {
		f.env.Runner = &execFailRunner{Fake: f.fake}
		Expect(f.run()).NotTo(Equal(0))
		Expect(f.fake.CommandLines()).To(ContainElement("docker rm " + nameOf(f.launched().Args)))
		Expect(dirNames(f.tmp)).To(BeEmpty())
	})

	It("CS-LNCH-083: when the reservation cannot be removed, the directory is kept", func() {
		f.fake.On("docker rm", "", execx.Fail(1))
		f.env.Runner = &execFailRunner{Fake: f.fake}
		Expect(f.run()).NotTo(Equal(0))
		dir := labelValue(f.launched().Args, launch.LabelShadowDir)
		Expect(dir).To(BeADirectory())
	})

	It("CS-LNCH-084: the would-be config hash leaves no shadow directory behind", func() {
		fl, err := scanLaunchArgs(nil)
		Expect(err).NotTo(HaveOccurred())
		hash, _ := wouldBeFingerprint(f.env, f.proj, fl, &cascade.Config{}, nil, nil)
		Expect(hash).NotTo(BeEmpty())
		Expect(dirNames(f.tmp)).To(BeEmpty())
	})
})

// A future fixture that forgets Env.TempRoot must fail loudly, never sweep the
// real temp root against a faked, empty docker ps (CS-LNCH-082).
var _ = Describe("test guard: an Env without TempRoot (CS-LNCH-082)", func() {
	It("CS-LNCH-082: a launch with an empty Env.TempRoot panics before sweeping or making anything", func() {
		f := newCLIFixture()
		f.env.TempRoot = ""
		Expect(func() { f.run() }).To(PanicWith(ContainSubstring("set Env.TempRoot")))
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix(sweepPS))
		}
	})
})
