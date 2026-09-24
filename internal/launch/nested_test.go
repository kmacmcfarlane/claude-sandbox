package launch_test

// Spec: spec/launch.feature (CS-LNCH-163, CS-LNCH-164) — a launcher inside a
// sandbox hands docker a resolved path as a bind source only when mountinfo
// shows the outer sandbox bound it in. mountinfo is always faked.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

// rootOnly is a mountinfo holding only the container's own root filesystem.
const rootOnly = "1 0 0:1 / / rw - overlay overlay rw\n"

// withBind adds a bind mount (ext4) at mp to rootOnly.
func withBind(mp string) string {
	return rootOnly + "2 1 0:2 /src " + mp + " rw,relatime - ext4 /dev/sda1 rw\n"
}

var _ = Describe("nested launches: resolved bind sources (CS-LNCH-163/164)", func() {
	var (
		base, home, proj, cfgDir, dotfiles string
		env                                map[string]string
		in                                 launch.Inputs
		errw                               *bytes.Buffer
		reads                              int
		mountinfo                          string
		mountErr                           error
	)

	BeforeEach(func() {
		var err error
		base, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		home, proj = filepath.Join(base, "home"), filepath.Join(base, "proj")
		cfgDir = filepath.Join(home, ".claude")
		dotfiles = filepath.Join(base, "dotfiles")
		for _, d := range []string{home, proj, cfgDir, dotfiles} {
			mkdir(d)
		}
		env = map[string]string{"CLAUDE_SANDBOX_PROJECT_DIR": "/outer/proj"}
		errw = &bytes.Buffer{}
		reads, mountinfo, mountErr = 0, rootOnly, nil
		in = launch.Inputs{
			ProjectDir: proj, Home: home, HostUID: 1000, HostGID: 1000, HostUser: "tester",
			Getenv:    func(k string) string { return env[k] },
			TempDir:   filepath.Join(base, "shadow"),
			ImageName: "img", Out: &bytes.Buffer{}, Err: errw,
			MountInfo: func() (string, error) { reads++; return mountinfo, mountErr },
		}
		mkdir(in.TempDir)
	})

	build := func() *launch.Plan {
		p, err := launch.Build(in)
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	Describe("CS-LNCH-163: the settings.json symlink target", func() {
		var link, target string
		BeforeEach(func() {
			link = filepath.Join(cfgDir, "settings.json")
			target = filepath.Join(dotfiles, "settings.json")
			touch(target, `{}`)
			Expect(os.Symlink(target, link)).To(Succeed())
		})

		It("CS-LNCH-163: a target the outer sandbox bound in is mounted as in CS-LNCH-069", func() {
			mountinfo = withBind(dotfiles)
			Expect(build().Volumes).To(ContainElement(target + ":" + target))
			Expect(errw.String()).To(BeEmpty())
			Expect(reads).To(Equal(1))
		})

		DescribeTable("CS-LNCH-163: a target not demonstrably host-visible is skipped with one warning, and the launch goes on",
			func(mi string, err error, reason string) {
				mountinfo, mountErr = mi, err
				p := build()
				for _, v := range p.Volumes {
					Expect(v).NotTo(HavePrefix(target + ":"))
				}
				w := errw.String()
				Expect(strings.Count(w, "\n")).To(Equal(1), w)
				Expect(w).To(ContainSubstring(link))
				Expect(w).To(ContainSubstring(target))
				Expect(w).To(ContainSubstring("does not mount at the same path (" + reason + ")"))
				Expect(w).To(ContainSubstring("without user settings"))
			},
			Entry("on the container's root filesystem", rootOnly, nil, "it is on the container's own root filesystem"),
			Entry("mountinfo unreadable", "", errors.New("boom"), "/proc/self/mountinfo is unreadable: boom"),
		)

		It("CS-LNCH-163: a target on a tmpfs inside the container is skipped", func() {
			mountinfo = rootOnly + "2 1 0:2 / " + dotfiles + " rw - tmpfs tmpfs rw\n"
			p := build()
			for _, v := range p.Volumes {
				Expect(v).NotTo(HavePrefix(target + ":"))
			}
			Expect(errw.String()).To(ContainSubstring("(it is on a tmpfs mounted at " + dotfiles + " inside the container)"))
		})

		It("CS-LNCH-163: a target under this launch's own same-path mount never reads mountinfo", func() {
			Expect(os.Remove(link)).To(Succeed())
			inCfg := filepath.Join(cfgDir, "settings.real.json")
			touch(inCfg, `{}`)
			Expect(os.Symlink(inCfg, link)).To(Succeed())
			build()
			Expect(reads).To(Equal(0))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-163: outside a sandbox mountinfo is never read and the target is mounted", func() {
			delete(env, "CLAUDE_SANDBOX_PROJECT_DIR")
			Expect(build().Volumes).To(ContainElement(target + ":" + target))
			Expect(reads).To(Equal(0))
		})

		It("CS-LNCH-163: a nested launch with no fake mountinfo panics under go test rather than read the real one", func() {
			in.MountInfo = nil
			Expect(func() { _, _ = launch.Build(in) }).To(PanicWith(ContainSubstring("pass a fake mountinfo")))
		})
	})

	Describe("CS-LNCH-164: the linked worktree's common git dir", func() {
		var common string
		BeforeEach(func() {
			common = filepath.Join(base, "repo", ".git")
			mkdir(common)
			in.Linked = &launch.LinkedWorktree{
				Top: proj, GitDir: filepath.Join(common, "worktrees", "proj"),
				CommonDir: common, Main: filepath.Join(base, "repo"),
			}
		})

		It("CS-LNCH-164: a common dir the outer sandbox bound in is mounted read-write as in CS-LNCH-071", func() {
			mountinfo = withBind(filepath.Join(base, "repo"))
			Expect(build().Volumes).To(ContainElement(common + ":" + common))
			Expect(errw.String()).To(BeEmpty())
		})

		It("CS-LNCH-164: a common dir on the container's own filesystem is not mounted, with one warning", func() {
			with := build()
			Expect(with.Volumes).NotTo(ContainElement(common + ":" + common))
			w := errw.String()
			Expect(strings.Count(w, "\n")).To(Equal(1), w)
			Expect(w).To(ContainSubstring("git dir " + common + " is not mounted"))
			Expect(w).To(ContainSubstring("does not mount it at the same path (it is on the container's own root filesystem)"))
			Expect(w).To(ContainSubstring("git cannot reach the repository in this session"))
			// Exactly the plan of a launch without the mount.
			in.Linked = nil
			in.TempDir = filepath.Join(base, "shadow2")
			mkdir(in.TempDir)
			Expect(with.ConfigHash).To(Equal(build().ConfigHash))
		})

		It("CS-LNCH-164: a common dir a same-path mount of this launch covers never reads mountinfo", func() {
			in.Linked.CommonDir = filepath.Join(proj, ".git-common")
			mkdir(in.Linked.CommonDir)
			build()
			Expect(reads).To(Equal(0))
			Expect(errw.String()).To(BeEmpty())
		})
	})
})
