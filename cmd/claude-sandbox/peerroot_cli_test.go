package main

// Spec: spec/host-dirs.feature (CS-DIR-010..019) — the shared peer registry's
// drain-then-switch move from ~/.cache/claude-sandbox/peers to
// ~/.local/state/claude-sandbox-peers, chosen under the launch lock from the
// bind sources discovery lists, and reused by the attach/join drift check.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/paths"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// mountsRow is a discovery row carrying every trailing field through the
// peerroot label and {{.Mounts}} (CS-DIR-011/012).
func mountsRow(name, project, class, state, keep, peerRoot string, mounts ...string) string {
	status := "Up 1 hour"
	if state == "exited" {
		status = "Exited (0) 1 second ago"
	}
	return strings.Join([]string{
		name, status, project, "claude", "", "v1", "", "", "", class, "",
		state, "2026-09-30 12:00:00 +0000 UTC", "", "", keep,
		"", "", "", "", "", peerRoot, strings.Join(mounts, ","),
	}, psSep)
}

var _ = Describe("peers root move: drain-then-switch (CS-DIR-010..019)", func() {
	var (
		f              *cliFixture
		home           string
		legacy, newer  string
		discoveryRows  string
		discoveryFails bool
	)

	BeforeEach(func() {
		f = newCLIFixture()
		// A short fixed-root home: past 103 bytes the bridge stands down for
		// length (CS-LNCH-055), whichever root is chosen.
		short, err := os.MkdirTemp("/tmp", "cs")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, short)
		short, err = filepath.EvalSymlinks(short)
		Expect(err).NotTo(HaveOccurred())
		home = filepath.Join(short, "h")
		f.envmap["HOME"] = home
		Expect(os.MkdirAll(filepath.Join(home, ".claude"), 0o755)).To(Succeed())
		legacy = filepath.Join(home, ".cache", "claude-sandbox", "peers")
		newer = filepath.Join(home, ".local", "state", "claude-sandbox-peers")
		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: true\n")
		discoveryRows, discoveryFails = "", false
		f.fake.OnFunc("label=claude-sandbox.project", func(c execx.Cmd) (string, error) {
			// Only the host-wide discovery under the launch lock fails; the
			// per-project lookup before it has its own fallbacks.
			perProject := strings.Contains(strings.Join(c.Args, " "), "label=claude-sandbox.project=")
			if discoveryFails && !perProject {
				return "", execx.Fail(1)
			}
			return discoveryRows, nil
		})
	})

	launch := func() []string {
		f.fake.Calls = nil
		f.out.Reset()
		f.errw.Reset()
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		return f.launched().Args
	}
	xdg := func(args []string) []string {
		var v []string
		for _, e := range argPairsCLI(args, "-e") {
			if x, ok := strings.CutPrefix(e, "XDG_RUNTIME_DIR="); ok {
				v = append(v, x)
			}
		}
		return v
	}
	expectRoot := func(args []string, root string) {
		Expect(xdg(args)).To(Equal([]string{root}))
		Expect(argPairsCLI(args, "-v")).To(ContainElement(root + ":" + root))
		Expect(argPairsCLI(args, "-v")).To(ContainElement(
			filepath.Join(root, "sessions") + ":" + filepath.Join(home, ".claude", "sessions")))
		Expect(labelValue(args, "claude-sandbox.peerroot")).To(Equal(root))
		Expect(labelValue(args, "claude-sandbox.registry")).To(Equal(filepath.Join(root, "sessions")))
	}

	It("CS-DIR-010: no container on the legacy root → the new root, and a plain banner", func() {
		args := launch()
		expectRoot(args, newer)
		Expect(f.out.String()).To(ContainSubstring("Peer registry: shared (" + newer + ")"))
		Expect(f.out.String()).NotTo(ContainSubstring("old location"))
		Expect(legacy).NotTo(BeADirectory(), "nothing is created at the legacy root")
		// The discovery that chose it is the one reservation already runs:
		// with --no-trunc and the trailing {{.Mounts}} field.
		var ps string
		for _, l := range f.fake.CommandLines() {
			if strings.Contains(l, "label=claude-sandbox.project") {
				ps = l
			}
		}
		Expect(ps).To(ContainSubstring("--no-trunc"))
		Expect(ps).To(ContainSubstring("{{.Mounts}}"))
	})

	It("CS-DIR-011: a container mounting the legacy root pins it, and the banner says so", func() {
		Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
		discoveryRows = mountsRow("cs-a", "/elsewhere", "3", "running", "", "",
			"/x/proj", filepath.Join(legacy, "sessions"), legacy) + "\n"
		args := launch()
		expectRoot(args, legacy)
		o := f.out.String()
		Expect(strings.Count(o, "Peer registry: shared (")).To(Equal(1))
		Expect(o).To(ContainSubstring("Peer registry: shared (" + legacy + ")"))
		Expect(o).To(ContainSubstring("This is the old location: 1 container(s) use it. It moves to " + newer +
			" at the first launch when none do (usually after a reboot)."))
		Expect(newer).NotTo(BeADirectory(), "the new root is not planted during the drain")
		Expect(legacy).To(BeADirectory(), "the launcher never deletes the legacy root")
	})

	It("CS-DIR-011: the sessions/ source alone pins, and so does an exited --rm row docker is still removing", func() {
		for _, row := range []string{
			mountsRow("cs-a", "/p", "3", "running", "", "", filepath.Join(legacy, "sessions")+"/"),
			mountsRow("cs-b", "/p", "4", "exited", "", "", legacy),
		} {
			discoveryRows = row + "\n"
			expectRoot(launch(), legacy)
		}
	})

	It("CS-DIR-012: containers that never bridged do not pin, labelled or not; every container says which root it took", func() {
		discoveryRows = strings.Join([]string{
			mountsRow("cs-a", "/p", "3", "running", "", "", "/x/proj", filepath.Join(home, ".claude")),
			mountsRow("cs-b", "/p", "4", "running", "", "none", "/y"),
			mountsRow("cs-c", "/p", "5", "running", "", newer, newer),
			// A path that merely starts with the legacy root's spelling.
			mountsRow("cs-d", "/p", "6", "running", "", "", legacy+"-other"),
		}, "\n") + "\n"
		expectRoot(launch(), newer)

		writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: false\n")
		args := launch()
		Expect(labelValue(args, "claude-sandbox.peerroot")).To(Equal("none"))
		Expect(xdg(args)).To(BeEmpty())
	})

	It("CS-DIR-013: another user's legacy root does not pin", func() {
		discoveryRows = mountsRow("cs-a", "/p", "3", "running", "", "",
			"/home/other/.cache/claude-sandbox/peers", "/home/other/.cache/claude-sandbox/peers/sessions") + "\n"
		expectRoot(launch(), newer)
	})

	It("CS-DIR-014: a failed discovery keeps an existing legacy root, with one warning", func() {
		Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
		discoveryFails = true
		args := launch()
		expectRoot(args, legacy)
		o := f.out.String()
		Expect(strings.Count(o, "could not list containers")).To(Equal(1))
		Expect(o).To(ContainSubstring("Warning: could not list containers; keeping the peer registry at " + legacy + " for this launch"))
	})

	It("CS-DIR-015: a failed discovery with no legacy directory (or a symlink there) takes the new root, silently", func() {
		discoveryFails = true
		expectRoot(launch(), newer)
		Expect(f.out.String()).NotTo(ContainSubstring("could not list containers"))

		target := filepath.Join(home, "elsewhere")
		Expect(os.MkdirAll(target, 0o700)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(legacy), 0o700)).To(Succeed())
		Expect(os.Symlink(target, legacy)).To(Succeed())
		expectRoot(launch(), newer)
	})

	It("CS-DIR-015: a failed discovery after the switch stays on the new root, though the legacy dir remains", func() {
		Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
		expectRoot(launch(), newer) // the switch: nothing mounts the legacy root
		discoveryFails = true
		expectRoot(launch(), newer)
		Expect(f.out.String()).NotTo(ContainSubstring("could not list containers"))
		Expect(legacy).To(BeADirectory())
	})

	It("CS-SESS-048, CS-DIR-011: the unserialized-launch warning says the peers root is unprotected too", func() {
		f.lock.err = errors.New("timed out after 30s")
		launch()
		Expect(f.errw.String()).To(ContainSubstring("pid class"))
		Expect(f.errw.String()).To(ContainSubstring("The shared peer registry's root is not protected either"))
	})

	Describe("the drift check (CS-DIR-017..019)", func() {
		driftHash := func(target *sessions.Session) string {
			fl, err := scanLaunchArgs(nil)
			Expect(err).NotTo(HaveOccurred())
			configFiles, err := paths.CollectUp(f.proj, paths.Config)
			Expect(err).NotTo(HaveOccurred())
			envFiles, err := paths.CollectUp(f.proj, paths.Env)
			Expect(err).NotTo(HaveOccurred())
			cfg, err := cascade.Load(configFiles)
			Expect(err).NotTo(HaveOccurred())
			hash, _ := wouldBeFingerprint(f.env, f.proj, fl, cfg, envFiles, nil, target)
			Expect(hash).NotTo(BeEmpty())
			return hash
		}

		It("CS-DIR-017: a container on the legacy root, labelled or not, reports no drift after the switch", func() {
			Expect(os.MkdirAll(legacy, 0o700)).To(Succeed())
			discoveryRows = mountsRow("cs-a", "/p", "3", "running", "", "", legacy) + "\n"
			args := launch()
			onLegacy := labelValue(args, "claude-sandbox.confighash")
			// Launches have switched: nothing pins the legacy root any more.
			discoveryRows = ""
			Expect(labelValue(launch(), "claude-sandbox.confighash")).NotTo(Equal(onLegacy),
				"the roots differ in the mount set")

			Expect(driftHash(&sessions.Session{PeerRoot: legacy})).To(Equal(onLegacy))
			Expect(driftHash(&sessions.Session{Mounts: []string{"/x", legacy}})).To(Equal(onLegacy))
			// A container without the label and without a legacy mount uses
			// the new root.
			Expect(driftHash(&sessions.Session{Mounts: []string{"/x"}})).NotTo(Equal(onLegacy))
		})

		It("CS-DIR-018: a container launched without the bridge still reports drift once the key is on", func() {
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: false\n")
			off := labelValue(launch(), "claude-sandbox.confighash")
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: true\n")
			Expect(driftHash(&sessions.Session{PeerRoot: "none"})).NotTo(Equal(off))
		})

		It("CS-DIR-019: the drift check creates no peer directory, yet a refused one still stands its bridge down", func() {
			_ = driftHash(&sessions.Session{PeerRoot: newer})
			Expect(newer).NotTo(BeADirectory())
			Expect(legacy).NotTo(BeADirectory())
			Expect(filepath.Join(home, ".claude", "sessions")).NotTo(BeADirectory())

			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: false\n")
			off := driftHash(nil)
			writeFile(filepath.Join(f.proj, ".claude-sandbox", "config.yaml"), "sharedPeerRegistry: true\n")
			on := driftHash(nil)
			Expect(on).NotTo(Equal(off))

			target := filepath.Join(home, "elsewhere")
			Expect(os.MkdirAll(target, 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(newer), 0o700)).To(Succeed())
			Expect(os.Symlink(target, newer)).To(Succeed())
			Expect(driftHash(nil)).To(Equal(off), "a symlinked root stands the would-be bridge down, as the launch would")
			fi, err := os.Stat(target)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)), "nothing re-moded through the link")
		})
	})
})
