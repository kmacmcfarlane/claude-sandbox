package hostdirs_test

// Spec: spec/host-dirs.feature (CS-DIR-001..006). Every directory is a
// GinkgoT().TempDir(); nothing touches the real $HOME.

import (
	"os"
	"path/filepath"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func mode(p string) os.FileMode {
	fi, err := os.Lstat(p)
	Expect(err).NotTo(HaveOccurred())
	return fi.Mode().Perm()
}

var _ = Describe("state and cache roots", func() {
	It("CS-DIR-001: the state root defaults to ~/.local/state/claude-sandbox", func() {
		Expect(hostdirs.StateRoot("/h", env(nil))).To(Equal("/h/.local/state/claude-sandbox"))
		Expect(hostdirs.StateRoot("/h", nil)).To(Equal("/h/.local/state/claude-sandbox"))
	})

	It("CS-DIR-001: an absolute XDG_STATE_HOME is honoured", func() {
		Expect(hostdirs.StateRoot("/h", env(map[string]string{"XDG_STATE_HOME": "/x/state"}))).
			To(Equal("/x/state/claude-sandbox"))
		Expect(hostdirs.StateRoot("/h", env(map[string]string{"XDG_STATE_HOME": "/x/state/"}))).
			To(Equal("/x/state/claude-sandbox"))
	})

	It("CS-DIR-001: the peers root is the pinned sibling of the default state root, whatever XDG_STATE_HOME says", func() {
		Expect(hostdirs.PeersRoot("/h")).To(Equal("/h/.local/state/claude-sandbox-peers"))
		// Never at or under the state root, default or relocated.
		for _, x := range []string{"", "/x/state", "/h/.local/state"} {
			sr := hostdirs.StateRoot("/h", env(map[string]string{"XDG_STATE_HOME": x}))
			Expect(hostdirs.PeersRoot("/h")).NotTo(Equal(sr))
			Expect(hostdirs.PeersRoot("/h")).NotTo(HavePrefix(sr + "/"))
		}
	})

	It("CS-DIR-002: a relative or empty XDG_STATE_HOME is ignored", func() {
		for _, x := range []string{"", "rel/state", "./state", "~/state"} {
			Expect(hostdirs.StateRoot("/h", env(map[string]string{"XDG_STATE_HOME": x}))).
				To(Equal("/h/.local/state/claude-sandbox"), "XDG_STATE_HOME=%q", x)
		}
	})

	It("CS-DIR-003: the cache root is ~/.cache/claude-sandbox and the launcher's roots still derive from it", func() {
		Expect(hostdirs.CacheRoot("/h")).To(Equal("/h/.cache/claude-sandbox"))
		// No XDG_CACHE_HOME input exists to honour: the signature takes none.
		Expect(launch.SandboxHomeRoot).To(Equal(hostdirs.CacheRootRel))
		Expect(launch.PackageCacheRoot).To(Equal(".cache/claude-sandbox"))
		Expect(launch.PeerRegistryRoot).To(Equal(".cache/claude-sandbox/peers"))
		Expect(launch.LaunchLockPath("/h")).To(Equal("/h/.cache/claude-sandbox/launch.lock"))
	})
})

var _ = Describe("EnsureOwnedDir", func() {
	var base string
	BeforeEach(func() { base = GinkgoT().TempDir() })

	It("CS-DIR-004: creates a missing directory and its parents 0700 as the invoking user", func() {
		dir := filepath.Join(base, "a", "b", "claude-sandbox")
		Expect(hostdirs.EnsureOwnedDir(dir, hostdirs.OwnedDirMode, nil)).To(Succeed())
		Expect(mode(dir)).To(Equal(os.FileMode(0o700)))
		fi, err := os.Lstat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(int(fi.Sys().(*syscall.Stat_t).Uid)).To(Equal(os.Getuid()))
	})

	It("CS-DIR-004: tightens an existing wider directory it owns", func() {
		dir := filepath.Join(base, "wide")
		Expect(os.Mkdir(dir, 0o755)).To(Succeed())
		Expect(os.Chmod(dir, 0o777)).To(Succeed())
		Expect(hostdirs.EnsureOwnedDir(dir, hostdirs.OwnedDirMode, nil)).To(Succeed())
		Expect(mode(dir)).To(Equal(os.FileMode(0o700)))
	})

	It("CS-DIR-004: a failing chmod is reported with the mode, through the seam", func() {
		dir := filepath.Join(base, "d")
		err := hostdirs.EnsureOwnedDir(dir, hostdirs.OwnedDirMode, &hostdirs.Ops{
			Chmod: func(name string, _ os.FileMode) error {
				return &os.PathError{Op: "chmod", Path: name, Err: syscall.EPERM}
			},
		})
		Expect(err).To(MatchError("restricting it to 0700: chmod " + dir + ": operation not permitted"))
	})

	Context("CS-DIR-005: refusals happen before any chmod", func() {
		var chmods []string
		var ops *hostdirs.Ops
		BeforeEach(func() {
			chmods = nil
			ops = &hostdirs.Ops{Chmod: func(name string, m os.FileMode) error {
				chmods = append(chmods, name)
				return os.Chmod(name, m)
			}}
		})

		It("CS-DIR-005: refuses a symlink to a directory and leaves its target's mode alone", func() {
			target := filepath.Join(base, "target")
			Expect(os.Mkdir(target, 0o755)).To(Succeed())
			Expect(os.Chmod(target, 0o755)).To(Succeed())
			link := filepath.Join(base, "link")
			Expect(os.Symlink(target, link)).To(Succeed())
			err := hostdirs.EnsureOwnedDir(link, hostdirs.OwnedDirMode, ops)
			Expect(err).To(MatchError(ContainSubstring("it is a symlink")))
			Expect(chmods).To(BeEmpty())
			Expect(mode(target)).To(Equal(os.FileMode(0o755)))
		})

		It("CS-DIR-005: refuses a regular file", func() {
			f := filepath.Join(base, "file")
			Expect(os.WriteFile(f, nil, 0o644)).To(Succeed())
			err := hostdirs.EnsureOwnedDir(f, hostdirs.OwnedDirMode, ops)
			Expect(err).To(MatchError(HavePrefix("creating it:")))
			Expect(chmods).To(BeEmpty())
			Expect(mode(f)).To(Equal(os.FileMode(0o644)))
		})

		It("CS-DIR-005: refuses a directory another uid owns", func() {
			dir := filepath.Join(base, "theirs")
			Expect(os.Mkdir(dir, 0o755)).To(Succeed())
			ops.Getuid = func() int { return os.Getuid() + 1 }
			err := hostdirs.EnsureOwnedDir(dir, hostdirs.OwnedDirMode, ops)
			Expect(err).To(MatchError(ContainSubstring("is owned by uid")))
			Expect(chmods).To(BeEmpty())
		})
	})
})

var _ = Describe("InSandbox", func() {
	It("CS-DIR-006: true exactly when CLAUDE_SANDBOX_PROJECT_DIR is non-empty", func() {
		Expect(hostdirs.InSandbox(env(map[string]string{"CLAUDE_SANDBOX_PROJECT_DIR": "/p"}))).To(BeTrue())
		Expect(hostdirs.InSandbox(env(map[string]string{"CLAUDE_SANDBOX_PROJECT_DIR": ""}))).To(BeFalse())
		Expect(hostdirs.InSandbox(env(nil))).To(BeFalse())
		Expect(hostdirs.InSandbox(nil)).To(BeFalse())
	})

	It("CS-DIR-006: other container markers do not count", func() {
		// /.dockerenv is never consulted: the function takes no filesystem
		// input, so a CI container without the sandbox's env is not a sandbox.
		Expect(hostdirs.InSandbox(env(map[string]string{
			"container": "docker", "XDG_RUNTIME_DIR": "/h/.cache/claude-sandbox/peers",
		}))).To(BeFalse())
	})
})
