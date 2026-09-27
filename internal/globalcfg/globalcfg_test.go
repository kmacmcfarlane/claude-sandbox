package globalcfg_test

// Spec: spec/global-config.feature (CS-GCFG). Every test works in a scratch
// HOME under GinkgoT().TempDir(); nothing reads or writes the real home or the
// real ~/.claude.json.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
)

func write(p, content string) {
	Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o600)).To(Succeed())
}

// scratchHome is a fresh, symlink-free home directory.
func scratchHome() string {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	home := filepath.Join(base, "home")
	Expect(os.MkdirAll(home, 0o755)).To(Succeed())
	return home
}

var _ = Describe("Classify (CS-GCFG-016..029)", func() {
	var home, link, target string
	BeforeEach(func() {
		home = scratchHome()
		link = filepath.Join(home, ".claude.json")
		target = filepath.Join(home, ".claude", ".claude.json")
	})

	It("CS-GCFG-016: a relative link to .claude/.claude.json, a regular file, is linked", func() {
		write(target, "{}")
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeLinked))
		Expect(l.Link).To(Equal(link))
		Expect(l.Target).To(Equal(target))
		Expect(l.Resolved).To(Equal(target))
	})

	It("CS-GCFG-017: an absolute link text, and an uncleaned relative one, are linked", func() {
		write(target, "{}")
		for _, text := range []string{target, "./.claude/../.claude/.claude.json"} {
			os.Remove(link)
			Expect(os.Symlink(text, link)).To(Succeed())
			l := globalcfg.Classify(home, "")
			Expect(l.Mode).To(Equal(globalcfg.ModeLinked), text)
			Expect(l.Resolved).To(Equal(target))
		}
	})

	It("CS-GCFG-017: the comparison is lexical: a link to an equivalent path through a symlink is refused", func() {
		write(target, "{}")
		alias := filepath.Join(filepath.Dir(home), "alias")
		Expect(os.Symlink(home, alias)).To(Succeed())
		Expect(os.Symlink(filepath.Join(alias, ".claude/.claude.json"), link)).To(Succeed())
		Expect(globalcfg.Classify(home, "").Mode).To(Equal(globalcfg.ModeRefused))
	})

	It("CS-GCFG-018: nested — the absolute link pidslot made is linked", func() {
		write(target, "{}")
		Expect(globalcfg.EnsureLink(home, target, &bytes.Buffer{}, nil)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeLinked))
		Expect(l.LinkText).To(Equal(target))
	})

	It("CS-GCFG-019: a two-level chain is refused, naming the rule", func() {
		write(target, "{}")
		mid := filepath.Join(home, "mid.json")
		Expect(os.Symlink(".claude/.claude.json", mid)).To(Succeed())
		Expect(os.Symlink("mid.json", link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		w := l.RefusedWarning()
		Expect(w).To(HavePrefix("WARNING: " + link + " (a symlink to mid.json) is not mounted"))
		Expect(w).To(ContainSubstring("directly"))
		Expect(w).To(ContainSubstring(target))
		Expect(strings.Count(w, "\n")).To(Equal(1), "one line")
	})

	It("CS-GCFG-020: a link to another regular file is refused", func() {
		other := filepath.Join(home, "dotfiles", "claude.json")
		write(other, "{}")
		Expect(os.Symlink(other, link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.RefusedWarning()).To(ContainSubstring(other))
	})

	It("CS-GCFG-021: a dangling link is refused", func() {
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.Problem).To(ContainSubstring("dangling"))
	})

	It("CS-GCFG-022: a link to a directory, or to a symlink, is refused", func() {
		Expect(os.MkdirAll(target, 0o755)).To(Succeed())
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.Problem).To(ContainSubstring("a directory"))

		Expect(os.Remove(target)).To(Succeed())
		write(filepath.Join(home, ".claude", "real.json"), "{}")
		Expect(os.Symlink("real.json", target)).To(Succeed())
		l = globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.Problem).To(ContainSubstring("a symlink"))
	})

	It("CS-GCFG-023: a symlinked config dir is refused", func() {
		real := filepath.Join(home, "real-claude")
		write(filepath.Join(real, ".claude.json"), "{}")
		Expect(os.Symlink("real-claude", filepath.Join(home, ".claude"))).To(Succeed())
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.RefusedWarning()).To(ContainSubstring("config dir " + filepath.Join(home, ".claude") + " is itself a symlink"))
	})

	It("CS-GCFG-024: a directory at ~/.claude.json is refused", func() {
		Expect(os.MkdirAll(link, 0o755)).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeRefused))
		Expect(l.Regular).To(BeFalse())
		Expect(l.RefusedWarning()).To(ContainSubstring("neither a regular file nor a symlink"))
	})

	It("CS-GCFG-025: a regular file is legacy; nothing is missing", func() {
		Expect(globalcfg.Classify(home, "").Mode).To(Equal(globalcfg.ModeMissing))
		write(link, "{}")
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeLegacy))
		Expect(l.Regular).To(BeTrue())
		Expect(l.SplitBrain).To(BeFalse())
	})

	It("CS-GCFG-026: a regular file beside ~/.claude/.claude.json is split brain, named with both mtimes", func() {
		write(link, "{}")
		write(target, "{}")
		Expect(os.Chtimes(link, time.Now(), time.Date(2026, 9, 26, 17, 28, 8, 0, time.UTC))).To(Succeed())
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeLegacy))
		Expect(l.SplitBrain).To(BeTrue())
		w := l.SplitBrainWarning()
		Expect(w).To(HavePrefix("WARNING: two global config files: " + link))
		Expect(w).To(ContainSubstring("modified 2026-09-26T17:28:08Z"))
		Expect(w).To(ContainSubstring(target))
		Expect(w).To(ContainSubstring("Claude Code uses " + link))
		Expect(w).To(ContainSubstring("every Claude session exited"))
		Expect(w).To(ContainSubstring(globalcfg.LinkCmd))
		Expect(w).To(ContainSubstring("adds checked commands"))
		Expect(w).NotTo(ContainSubstring("global-config migrate"), "no command this release does not ship")
	})

	It("CS-GCFG-027: CLAUDE_CONFIG_DIR set is out of scope, whatever the files", func() {
		write(target, "{}")
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		Expect(globalcfg.Classify(home, filepath.Join(home, ".claude")).Mode).To(Equal(globalcfg.ModeRelocated))
	})

	It("CS-GCFG-028: a relative or ~ CLAUDE_CONFIG_DIR is suspicious; an absolute one is not", func() {
		for _, v := range []string{"rel/cfg", "~/.claude", "/home/x/~cfg"} {
			Expect(globalcfg.SuspiciousConfigDir(v)).To(BeTrue(), v)
		}
		for _, v := range []string{"", "/home/x/.claude-sussex"} {
			Expect(globalcfg.SuspiciousConfigDir(v)).To(BeFalse(), v)
		}
		Expect(globalcfg.ConfigDirWarning("~/.claude")).To(HavePrefix(`WARNING: CLAUDE_CONFIG_DIR="~/.claude" is not a plain absolute path`))
	})

	It("CS-GCFG-029: .config.json wins over a valid link", func() {
		write(target, "{}")
		Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
		write(filepath.Join(home, ".claude", ".config.json"), "{}")
		l := globalcfg.Classify(home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeConfigJSON))
		Expect(l.Regular).To(BeFalse(), "a link is never mounted")
		Expect(l.ConfigJSONNote()).To(ContainSubstring(".config.json exists"))
	})
})

var _ = Describe("EnsureLink (CS-GCFG-033..037)", func() {
	var home, link, target string
	var warn *bytes.Buffer
	BeforeEach(func() {
		home = scratchHome()
		link = filepath.Join(home, ".claude.json")
		target = filepath.Join(home, ".claude", ".claude.json")
		write(target, `{"oauthAccount":{}}`)
		warn = &bytes.Buffer{}
	})

	readlink := func() string {
		t, err := os.Readlink(link)
		Expect(err).NotTo(HaveOccurred())
		return t
	}
	leftovers := func() []string {
		m, _ := filepath.Glob(link + ".link-*")
		return m
	}

	It("CS-GCFG-033: creates the link through a temp link and a rename", func() {
		var renamed []string
		ops := &globalcfg.LinkOps{Rename: func(a, b string) error { renamed = append(renamed, a+" -> "+b); return os.Rename(a, b) }}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(Succeed())
		Expect(readlink()).To(Equal(target))
		Expect(renamed).To(HaveLen(1))
		Expect(renamed[0]).To(MatchRegexp(`^` + regexpQuote(link) + `\.link-\d+-[0-9a-f]{8} -> ` + regexpQuote(link) + `$`))
		Expect(leftovers()).To(BeEmpty())
		Expect(warn.String()).To(BeEmpty())
	})

	It("CS-GCFG-033: an existing correct link is left alone (restart, join)", func() {
		Expect(os.Symlink(target, link)).To(Succeed())
		ops := &globalcfg.LinkOps{
			Symlink: func(string, string) error { Fail("no new link"); return nil },
			Rename:  func(string, string) error { Fail("no rename"); return nil },
		}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(Succeed())
		Expect(readlink()).To(Equal(target))
	})

	It("CS-GCFG-034: a wrong link, dangling included, is replaced", func() {
		for _, wrong := range []string{"/elsewhere/.claude.json", ".claude/.claude.json"} {
			os.Remove(link)
			Expect(os.Symlink(wrong, link)).To(Succeed())
			Expect(globalcfg.EnsureLink(home, target, warn, nil)).To(Succeed())
			Expect(readlink()).To(Equal(target), wrong)
		}
		Expect(leftovers()).To(BeEmpty())
	})

	It("CS-GCFG-035: an image-supplied regular file is hard-linked aside, then replaced", func() {
		write(link, "image leftover")
		fi, _ := os.Stat(link)
		now := time.UnixMilli(1790000000123)
		Expect(globalcfg.EnsureLink(home, target, warn, &globalcfg.LinkOps{Now: func() time.Time { return now }})).To(Succeed())
		Expect(readlink()).To(Equal(target))
		asides, _ := filepath.Glob(link + ".replaced-1790000000123-*")
		Expect(asides).To(HaveLen(1))
		aside := asides[0]
		Expect(aside).To(MatchRegexp(`\.replaced-1790000000123-\d+-[0-9a-f]{8}$`))
		b, err := os.ReadFile(aside)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("image leftover"))
		ai, _ := os.Stat(aside)
		Expect(os.SameFile(fi, ai)).To(BeTrue(), "a hard link, not a copy")
		Expect(warn.String()).To(ContainSubstring("kept at " + aside))
		Expect(strings.Count(warn.String(), "\n")).To(Equal(1))
	})

	It("CS-GCFG-035: when the hard link fails it copies and fsyncs instead", func() {
		write(link, "image leftover")
		now := time.UnixMilli(42)
		ops := &globalcfg.LinkOps{
			Now:  func() time.Time { return now },
			Link: func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} },
		}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(Succeed())
		asides, _ := filepath.Glob(link + ".replaced-42-*")
		Expect(asides).To(HaveLen(1))
		b, err := os.ReadFile(asides[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("image leftover"))
		Expect(readlink()).To(Equal(target))
	})

	It("CS-GCFG-035: when the hard link fails because a racing helper already linked, that is success", func() {
		write(link, "image leftover")
		ops := &globalcfg.LinkOps{
			Link: func(string, string) error {
				// The racer: moves the file away and puts the link in.
				Expect(os.Remove(link)).To(Succeed())
				Expect(os.Symlink(target, link)).To(Succeed())
				return &os.LinkError{Op: "link", Err: syscall.ENOENT}
			},
			Symlink: func(string, string) error { Fail("no second swap"); return nil },
		}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(Succeed())
		Expect(readlink()).To(Equal(target))
		asides, _ := filepath.Glob(link + ".replaced-*")
		Expect(asides).To(BeEmpty())
	})

	It("CS-GCFG-035: the path is never missing while a regular file is replaced", func() {
		write(link, "image leftover")
		var sawMissing bool
		check := func() {
			if _, err := os.Lstat(link); err != nil {
				sawMissing = true
			}
		}
		ops := &globalcfg.LinkOps{
			Link:    func(a, b string) error { check(); err := os.Link(a, b); check(); return err },
			Symlink: func(a, b string) error { check(); err := os.Symlink(a, b); check(); return err },
			Rename:  func(a, b string) error { check(); err := os.Rename(a, b); check(); return err },
		}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(Succeed())
		Expect(sawMissing).To(BeFalse())
	})

	It("CS-GCFG-036: fails for a target other than $HOME/.claude/.claude.json, a relative one, an unset HOME", func() {
		other := filepath.Join(home, ".claude", "other.json")
		write(other, "{}")
		for _, t := range []string{other, ".claude/.claude.json", filepath.Join(home, ".ssh", "id_ed25519")} {
			Expect(globalcfg.EnsureLink(home, t, warn, nil)).To(MatchError(ContainSubstring("must be "+target)), t)
		}
		_, lerr := os.Lstat(link)
		Expect(errors.Is(lerr, os.ErrNotExist)).To(BeTrue(), "nothing made")
		Expect(globalcfg.EnsureLink(home, filepath.Join(home, ".claude", "x", "..", ".claude.json"), warn, nil)).To(Succeed(), "compared cleaned")
		Expect(globalcfg.EnsureLink("", target, warn, nil)).To(MatchError("HOME is not set"))
	})

	It("CS-GCFG-036: fails for a missing or non-regular target and a directory in the way", func() {
		Expect(os.Remove(target)).To(Succeed())
		err := globalcfg.EnsureLink(home, target, warn, nil)
		Expect(err).To(MatchError(ContainSubstring(target + ": no such file or directory")))
		_, lerr := os.Lstat(link)
		Expect(errors.Is(lerr, os.ErrNotExist)).To(BeTrue(), "nothing made")

		Expect(os.MkdirAll(target, 0o755)).To(Succeed())
		Expect(globalcfg.EnsureLink(home, target, warn, nil)).To(MatchError(ContainSubstring("not a regular file")))
		Expect(os.Remove(target)).To(Succeed())
		write(target, "{}")

		Expect(os.MkdirAll(link, 0o755)).To(Succeed())
		Expect(globalcfg.EnsureLink(home, target, warn, nil)).To(MatchError(ContainSubstring(link + ": neither a regular file nor a symlink")))
	})

	It("CS-GCFG-036: fails when $HOME is not writable, leaving nothing behind", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		Expect(os.Chmod(home, 0o500)).To(Succeed())
		DeferCleanup(func() { os.Chmod(home, 0o755) })
		err := globalcfg.EnsureLink(home, target, warn, nil)
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(err.Error()).To(HavePrefix(link + ".link-"))
		_, lerr := os.Lstat(link)
		Expect(errors.Is(lerr, os.ErrNotExist)).To(BeTrue())
	})

	It("CS-GCFG-036: a failed rename removes its temp link", func() {
		ops := &globalcfg.LinkOps{Rename: func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EBUSY} }}
		Expect(globalcfg.EnsureLink(home, target, warn, ops)).To(MatchError(link + ": device or resource busy"))
		Expect(leftovers()).To(BeEmpty())
	})

	It("CS-GCFG-036: under go test the real home panics before anything is touched", func() {
		real, err := os.UserHomeDir()
		if err != nil || real == "" {
			Skip("no home directory")
		}
		ops := &globalcfg.LinkOps{
			Lstat:   func(string) (os.FileInfo, error) { Fail("touched the real home"); return nil, nil },
			Symlink: func(string, string) error { Fail("touched the real home"); return nil },
		}
		Expect(func() {
			_ = globalcfg.EnsureLink(real, filepath.Join(real, ".claude", ".claude.json"), warn, ops)
		}).To(PanicWith(ContainSubstring("under the real home")))
	})

	It("CS-GCFG-037: racing helpers all succeed and leave the one correct link", func() {
		var wg sync.WaitGroup
		errs := make([]error, 16)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = globalcfg.EnsureLink(home, target, &bytes.Buffer{}, nil)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(readlink()).To(Equal(target))
		Expect(leftovers()).To(BeEmpty())
	})
})

var _ = Describe("ExitMessage (CS-GCFG-038/039)", func() {
	It("CS-GCFG-038, CS-GCFG-039: says likely, points at the prefix, names both fixes and docker rm", func() {
		m := globalcfg.ExitMessage("claude-sandbox-x-otter")
		Expect(m).To(HavePrefix("The session exited with 78, likely the global-config link check"))
		Expect(m).To(ContainSubstring(`"claude-sandbox: global config link:"`))
		Expect(m).To(ContainSubstring("every Claude session exited"))
		Expect(m).To(ContainSubstring("copy ~/.claude.json somewhere outside ~/.claude/ first"))
		// The guards (review round 2): exact text.
		Expect(m).To(ContainSubstring("test -f ~/.claude.json && ! test -L ~/.claude.json && ! test -e ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && mv ~/.claude.json ~/.claude/.claude.json && ln -s .claude/.claude.json ~/.claude.json"))
		Expect(m).To(ContainSubstring("test -L ~/.claude.json && test -f ~/.claude/.claude.json && ! test -L ~/.claude/.claude.json && rm ~/.claude.json && mv ~/.claude/.claude.json ~/.claude.json"))
		Expect(m).NotTo(ContainSubstring("mv -n"))
		Expect(m).NotTo(ContainSubstring("global-config migrate"))
		Expect(m).To(ContainSubstring("docker rm claude-sandbox-x-otter"))
	})

	It("CS-GCFG-026: the split-brain warning says to keep a copy first and gives the guarded link step", func() {
		home := scratchHome()
		write(filepath.Join(home, ".claude.json"), "{}")
		write(filepath.Join(home, ".claude", ".claude.json"), "{}")
		w := globalcfg.Classify(home, "").SplitBrainWarning()
		Expect(w).To(ContainSubstring("copy ~/.claude.json somewhere outside ~/.claude/ first"))
		Expect(w).To(ContainSubstring(globalcfg.LinkCmd))
		Expect(w).To(ContainSubstring("test -f ~/.claude.json && ! test -L ~/.claude.json"))
	})

	Describe("CS-GCFG-038: the printed steps, run in a scratch HOME", func() {
		var home, link, target string
		BeforeEach(func() {
			home = scratchHome()
			link = filepath.Join(home, ".claude.json")
			target = filepath.Join(home, ".claude", ".claude.json")
			Expect(os.MkdirAll(filepath.Dir(target), 0o755)).To(Succeed())
		})
		run := func(cmd string) error {
			c := exec.Command("bash", "--noprofile", "--norc", "-c", cmd)
			c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
			return c.Run()
		}
		content := func(p string) string {
			b, err := os.ReadFile(p)
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("CS-GCFG-038: undo after a manual revert deletes nothing", func() {
			write(link, "the only config")
			Expect(run(globalcfg.UnlinkCmd)).NotTo(Succeed())
			Expect(content(link)).To(Equal("the only config"))
		})

		It("CS-GCFG-038: undo on a proper link restores the regular file", func() {
			write(target, "cfg")
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
			Expect(run(globalcfg.UnlinkCmd)).To(Succeed())
			fi, err := os.Lstat(link)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().IsRegular()).To(BeTrue())
			Expect(content(link)).To(Equal("cfg"))
		})

		It("CS-GCFG-038: link on a dangling link changes nothing (no chain)", func() {
			Expect(os.Symlink(".claude/.claude.json", link)).To(Succeed())
			Expect(run(globalcfg.LinkCmd)).NotTo(Succeed())
			text, _ := os.Readlink(link)
			Expect(text).To(Equal(".claude/.claude.json"))
			_, err := os.Lstat(target)
			Expect(os.IsNotExist(err)).To(BeTrue(), "nothing moved into ~/.claude/")
		})

		It("CS-GCFG-038: link never overwrites an existing target", func() {
			write(link, "new")
			write(target, "existing")
			Expect(run(globalcfg.LinkCmd)).NotTo(Succeed())
			Expect(content(target)).To(Equal("existing"))
			Expect(content(link)).To(Equal("new"))
		})

		It("CS-GCFG-038: link on a regular file with no target makes the linked layout", func() {
			write(link, "cfg")
			Expect(run(globalcfg.LinkCmd)).To(Succeed())
			Expect(globalcfg.Classify(home, "").Mode).To(Equal(globalcfg.ModeLinked))
			Expect(content(target)).To(Equal("cfg"))
		})
	})
})

func regexpQuote(s string) string {
	r := strings.NewReplacer(`.`, `\.`, `-`, `\-`, `+`, `\+`, `(`, `\(`, `)`, `\)`)
	return r.Replace(s)
}
