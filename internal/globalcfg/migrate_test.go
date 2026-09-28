package globalcfg_test

// Spec: spec/global-config.feature CS-GCFG-041..055 — the host commands
// global-config migrate|revert|accept. Every test uses a scratch HOME and a
// scratch state root under GinkgoT().TempDir(), and execx.Fake for docker,
// pgrep and claude; nothing touches the real home, ~/.claude.json or state.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
)

// noClaude is a Runner whose "claude" cannot be started (not on PATH).
type noClaude struct{ *execx.Fake }

func (n noClaude) Start(c execx.Cmd) (execx.Process, error) {
	if c.Name == "claude" {
		return nil, errors.New(`exec: "claude": executable file not found in $PATH`)
	}
	return n.Fake.Start(c)
}

type swapFixture struct {
	home, state, link, target string
	fake                      *execx.Fake
	out, errw                 *bytes.Buffer
}

func newSwapFixture() *swapFixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &swapFixture{
		home:  filepath.Join(base, "home"),
		state: filepath.Join(base, "state"),
		fake:  &execx.Fake{},
		out:   &bytes.Buffer{},
		errw:  &bytes.Buffer{},
	}
	Expect(os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700)).To(Succeed())
	f.link = filepath.Join(f.home, ".claude.json")
	f.target = filepath.Join(f.home, ".claude", ".claude.json")
	// The verified version, so only the tests about it see its warning.
	f.fake.On("claude --version", globalcfg.VerifiedClaudeVersion+" (Claude Code)\n", nil)
	return f
}

func (f *swapFixture) opts() globalcfg.SwapOptions {
	return globalcfg.SwapOptions{
		Home: f.home, StateRoot: f.state, Runner: f.fake,
		Out: f.out, Err: f.errw, UID: 1000,
		LockWait: 200 * time.Millisecond,
	}
}

func (f *swapFixture) storeDir() string {
	return filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(f.link))
}

func (f *swapFixture) entries(prefix string) []string {
	return (&globalcfg.Store{Dir: f.storeDir()}).List(prefix)
}

func read(p string) string {
	b, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

func mode(p string) os.FileMode {
	fi, err := os.Lstat(p)
	Expect(err).NotTo(HaveOccurred())
	return fi.Mode().Perm()
}

func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// leftovers lists the swap's temp names in $HOME.
func leftoversIn(home string) []string {
	var out []string
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		n := e.Name()
		if strings.Contains(n, ".migrate-") || strings.Contains(n, ".revert-") || strings.HasSuffix(n, ".lock") {
			out = append(out, n)
		}
	}
	return out
}

func expectRefused(err error, subs ...string) {
	var r *globalcfg.Refused
	ExpectWithOffset(1, errors.As(err, &r)).To(BeTrue(), "want a refusal, got %v", err)
	for _, s := range subs {
		ExpectWithOffset(1, r.Msg).To(ContainSubstring(s))
	}
}

const cfg = `{"projects":{"a":{},"b":{}},"oauthAccount":{"emailAddress":"secret-value"},"hasCompletedOnboarding":true,"numStartups":918273}`

var _ = Describe("global-config migrate (CS-GCFG-041..051)", func() {
	var f *swapFixture
	BeforeEach(func() { f = newSwapFixture() })

	It("CS-GCFG-041: a legacy file becomes the linked layout, with a pre-migration copy and the follow-up steps", func() {
		write(f.link, cfg)
		Expect(globalcfg.Migrate(f.opts())).To(Succeed(), f.errw.String())
		text, err := os.Readlink(f.link)
		Expect(err).NotTo(HaveOccurred())
		Expect(text).To(Equal(".claude/.claude.json"))
		Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLinked))
		Expect(read(f.target)).To(Equal(cfg))
		Expect(mode(f.target)).To(Equal(os.FileMode(0o600)))
		pre := f.entries(globalcfg.PreMigratePrefix)
		Expect(pre).To(HaveLen(1))
		Expect(read(pre[0])).To(Equal(cfg))
		Expect(leftoversIn(f.home)).To(BeEmpty(), "no temp link and the lock released")
		out := f.out.String()
		Expect(out).To(ContainSubstring("Migrated: " + f.link))
		Expect(out).To(ContainSubstring("Pre-migration copy: " + pre[0]))
		Expect(out).To(ContainSubstring("Update every claude-sandbox launcher"))
		Expect(out).To(ContainSubstring(globalcfg.SmokeTest))
		Expect(f.errw.String()).To(BeEmpty())
		Expect(f.fake.CommandLines()).To(ContainElement(HavePrefix("docker ps -a --no-trunc --format {{.Names}}\x1f{{.Mounts}}")))
	})

	Describe("CS-GCFG-042: layouts it cannot migrate", func() {
		expectUntouched := func() {
			Expect(exists(f.state)).To(BeFalse(), "nothing written under the state root")
			Expect(leftoversIn(f.home)).To(BeEmpty())
			Expect(f.fake.CommandLines()).To(BeEmpty(), "refused before any docker call")
		}

		It("CS-GCFG-042: CLAUDE_CONFIG_DIR set refuses migrate and revert, naming it and .envrc", func() {
			write(f.link, cfg)
			o := f.opts()
			o.ConfigDirEnv = "/work/.claude-work"
			expectRefused(globalcfg.Migrate(o), `"/work/.claude-work"`, ".envrc")
			expectRefused(globalcfg.Revert(o), `"/work/.claude-work"`, ".envrc")
			Expect(read(f.link)).To(Equal(cfg))
			expectUntouched()
		})

		It("CS-GCFG-042: a ~/.claude/.config.json refuses", func() {
			write(f.link, cfg)
			write(filepath.Join(f.home, ".claude", ".config.json"), "{}")
			expectRefused(globalcfg.Migrate(f.opts()), ".config.json")
			expectUntouched()
		})

		It("CS-GCFG-042: an already linked layout is a no-op success", func() {
			write(f.target, cfg)
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			Expect(f.out.String()).To(ContainSubstring("already a link"))
			expectUntouched()
		})

		It("CS-GCFG-042: a missing file, a dangling link and an unparseable file refuse", func() {
			expectRefused(globalcfg.Migrate(f.opts()), "does not exist")
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			expectRefused(globalcfg.Migrate(f.opts()), "dangling")
			Expect(os.Remove(f.link)).To(Succeed())
			write(f.link, `{"projects":`)
			expectRefused(globalcfg.Migrate(f.opts()), "does not parse")
			Expect(read(f.link)).To(Equal(`{"projects":`))
			expectUntouched()
		})

		It("CS-GCFG-042: a missing or symlinked config dir, or a directory at the target, refuses", func() {
			write(f.link, cfg)
			Expect(os.Mkdir(f.target, 0o700)).To(Succeed())
			expectRefused(globalcfg.Migrate(f.opts()), "not a regular file")
			Expect(os.Remove(f.target)).To(Succeed())

			real := filepath.Join(filepath.Dir(f.home), "elsewhere")
			Expect(os.Rename(filepath.Join(f.home, ".claude"), real)).To(Succeed())
			expectRefused(globalcfg.Migrate(f.opts()), "config dir")
			Expect(os.Symlink(real, filepath.Join(f.home, ".claude"))).To(Succeed())
			expectRefused(globalcfg.Migrate(f.opts()), "is a symlink")
			Expect(read(f.link)).To(Equal(cfg))
			expectUntouched()
		})
	})

	It("CS-GCFG-043: an identical existing target is kept and only the link is made", func() {
		write(f.link, cfg)
		write(f.target, cfg)
		before, _ := os.Stat(f.target)
		Expect(globalcfg.Migrate(f.opts())).To(Succeed(), f.errw.String())
		after, _ := os.Stat(f.target)
		Expect(os.SameFile(before, after)).To(BeTrue(), "the existing file is kept, not rewritten")
		Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLinked))
	})

	It("CS-GCFG-043: a different existing target refuses naming both, and neither changes", func() {
		write(f.link, cfg)
		write(f.target, "{}")
		expectRefused(globalcfg.Migrate(f.opts()), f.link, f.target, "split brain")
		Expect(read(f.link)).To(Equal(cfg))
		Expect(read(f.target)).To(Equal("{}"))
	})

	Describe("CS-GCFG-045..047: containers", func() {
		BeforeEach(func() { write(f.link, cfg) })

		It("CS-GCFG-045: a container mounting the file refuses, matched by exact source; others do not", func() {
			f.fake.On("docker ps", strings.Join([]string{
				"cs-otter\x1f/proj,/home/x/.claude," + f.link + ",/tmp/claude-sandbox123/CLAUDE.md",
				"cs-kept\x1f" + f.link + "/",
				"cs-near\x1f" + f.link + ".bak,/other" + f.link,
				"unrelated\x1f",
			}, "\n")+"\n", nil)
			err := globalcfg.Migrate(f.opts())
			expectRefused(err, "cs-otter, cs-kept", "--force", "tmux kill-server")
			Expect(err.Error()).NotTo(ContainSubstring("cs-near"))
			Expect(err.Error()).NotTo(ContainSubstring("unrelated"))
			Expect(read(f.link)).To(Equal(cfg))
			Expect(exists(f.target)).To(BeFalse())
			Expect(exists(f.state)).To(BeFalse())
		})

		It("CS-GCFG-045: a container mounting the link's target counts too", func() {
			f.fake.On("docker ps", "cs-old\x1f"+f.target+"\n", nil)
			expectRefused(globalcfg.Migrate(f.opts()), "cs-old")
		})

		It("CS-GCFG-045: a failed listing refuses; --force goes on with a warning", func() {
			f.fake.On("docker ps", "", execx.Fail(1))
			expectRefused(globalcfg.Migrate(f.opts()), "cannot tell", "--force")
			Expect(read(f.link)).To(Equal(cfg))
			o := f.opts()
			o.Force = true
			Expect(globalcfg.Migrate(o)).To(Succeed())
			Expect(f.errw.String()).To(ContainSubstring("WARNING: cannot tell"))
		})

		It("CS-GCFG-047: migrate --force names the containers and says they keep the old inode", func() {
			f.fake.On("docker ps", "cs-otter\x1f"+f.link+"\n", nil)
			o := f.opts()
			o.Force = true
			Expect(globalcfg.Migrate(o)).To(Succeed())
			w := f.errw.String()
			Expect(w).To(ContainSubstring("WARNING: --force"))
			Expect(w).To(ContainSubstring("cs-otter"))
			Expect(w).To(ContainSubstring("old inode"))
			Expect(w).To(ContainSubstring("writes are lost"))
			Expect(strings.Count(w, "WARNING")).To(Equal(1))
			Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLinked))
		})
	})

	Describe("CS-GCFG-048, CS-GCFG-049: advisory checks", func() {
		BeforeEach(func() { write(f.link, cfg) })

		It("CS-GCFG-048: host claude processes get one warning, and the migration goes on", func() {
			f.fake.On("pgrep -u 1000 -x claude", "123\n456\n", nil)
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			Expect(f.errw.String()).To(ContainSubstring("WARNING: 2 host claude process(es) are running (pids 123, 456)"))
			Expect(globalcfg.Classify(f.home, "").Mode).To(Equal(globalcfg.ModeLinked))
		})

		It("CS-GCFG-048: no match, or no pgrep, prints nothing", func() {
			f.fake.On("pgrep", "", execx.Fail(1))
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-GCFG-049: another host version warns, naming both and the smoke test, and never refuses", func() {
			f.fake = &execx.Fake{}
			f.fake.On("claude --version", "2.1.300 (Claude Code)\n", nil)
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			w := f.errw.String()
			Expect(w).To(ContainSubstring("verified with Claude Code " + globalcfg.VerifiedClaudeVersion + "; the host runs 2.1.300"))
			Expect(w).To(ContainSubstring(globalcfg.SmokeTest))
		})

		It("CS-GCFG-049: an unreadable version warns with unknown", func() {
			f.fake = &execx.Fake{}
			f.fake.On("claude --version", "", execx.Fail(1))
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			Expect(f.errw.String()).To(ContainSubstring("the host runs unknown"))
		})

		It("CS-GCFG-049: no claude on PATH prints nothing", func() {
			o := f.opts()
			o.Runner = noClaude{&execx.Fake{}}
			Expect(globalcfg.Migrate(o)).To(Succeed())
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-GCFG-049: revert does not check the version", func() {
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			f.fake = &execx.Fake{}
			Expect(globalcfg.Revert(f.opts())).To(Succeed())
			for _, l := range f.fake.CommandLines() {
				Expect(l).NotTo(HavePrefix("claude "))
			}
		})
	})

	Describe("CS-GCFG-050: the lock", func() {
		BeforeEach(func() { write(f.link, cfg) })
		lockPath := func() string { return f.link + ".lock" }

		It("CS-GCFG-050: the lock is held during the swap and removed after", func() {
			o := f.opts()
			held := false
			o.BeforeSwap = func() { held = exists(lockPath()) }
			Expect(globalcfg.Migrate(o)).To(Succeed())
			Expect(held).To(BeTrue())
			Expect(exists(lockPath())).To(BeFalse())
		})

		It("CS-GCFG-050: a fresh lock held past the wait refuses and changes nothing, leaving that lock alone", func() {
			Expect(os.Mkdir(lockPath(), 0o755)).To(Succeed())
			expectRefused(globalcfg.Migrate(f.opts()), "held by another process")
			Expect(exists(lockPath())).To(BeTrue(), "not ours to remove")
			Expect(read(f.link)).To(Equal(cfg))
			Expect(exists(f.target)).To(BeFalse())
		})

		It("CS-GCFG-050: a stale lock (mtime older than 10 s) is reclaimed", func() {
			Expect(os.Mkdir(lockPath(), 0o755)).To(Succeed())
			old := time.Now().Add(-time.Minute)
			Expect(os.Chtimes(lockPath(), old, old)).To(Succeed())
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			Expect(exists(lockPath())).To(BeFalse())
		})

		It("CS-GCFG-050: the lock's mtime is refreshed while held", func() {
			o := f.opts()
			o.LockRefresh = 10 * time.Millisecond
			var refreshed bool
			o.BeforeSwap = func() {
				old := time.Now().Add(-time.Hour)
				Expect(os.Chtimes(lockPath(), old, old)).To(Succeed())
				Eventually(func() time.Time {
					fi, err := os.Stat(lockPath())
					Expect(err).NotTo(HaveOccurred())
					return fi.ModTime()
				}).WithTimeout(2 * time.Second).Should(BeTemporally(">", time.Now().Add(-time.Minute)))
				refreshed = true
			}
			Expect(globalcfg.Migrate(o)).To(Succeed())
			Expect(refreshed).To(BeTrue())
		})

		It("CS-GCFG-050: the lock is removed after an abort too", func() {
			o := f.opts()
			o.BeforeSwap = func() { write(f.link, cfg+" ") }
			Expect(globalcfg.Migrate(o)).NotTo(Succeed())
			Expect(exists(lockPath())).To(BeFalse())
		})
	})

	Describe("CS-GCFG-051: aborts", func() {
		BeforeEach(func() { write(f.link, cfg) })

		It("CS-GCFG-051: a source written during the swap aborts, removing the copy and keeping the file", func() {
			o := f.opts()
			o.BeforeSwap = func() { write(f.link, `{"changed":true}`) }
			expectRefused(globalcfg.Migrate(o), "changed during the migration")
			Expect(isLink(f.link)).To(BeFalse())
			Expect(read(f.link)).To(Equal(`{"changed":true}`))
			Expect(exists(f.target)).To(BeFalse(), "the copy it made is removed")
			Expect(leftoversIn(f.home)).To(BeEmpty())
		})

		It("CS-GCFG-051: an identical target that was already there is not removed by an abort", func() {
			write(f.target, cfg)
			o := f.opts()
			o.BeforeSwap = func() { write(f.link, `{"changed":true}`) }
			Expect(globalcfg.Migrate(o)).NotTo(Succeed())
			Expect(read(f.target)).To(Equal(cfg))
		})

		It("CS-GCFG-051: steps under the lock past the 5 s bound abort before the rename", func() {
			o := f.opts()
			o.LockWait = time.Hour
			t := time.Now()
			o.Now = func() time.Time { t = t.Add(3 * time.Second); return t }
			expectRefused(globalcfg.Migrate(o), "longer than 5s")
			Expect(isLink(f.link)).To(BeFalse())
			Expect(exists(f.target)).To(BeFalse())
			Expect(leftoversIn(f.home)).To(BeEmpty())
		})

		It("CS-GCFG-051: a target written during a revert aborts it, and the link stays", func() {
			Expect(globalcfg.Migrate(f.opts())).To(Succeed())
			o := f.opts()
			o.BeforeSwap = func() { write(f.target, `{"changed":true}`) }
			expectRefused(globalcfg.Revert(o), "changed during the revert")
			Expect(isLink(f.link)).To(BeTrue())
			Expect(read(f.target)).To(Equal(`{"changed":true}`))
			Expect(leftoversIn(f.home)).To(BeEmpty())
		})
	})
})

var _ = Describe("global-config revert (CS-GCFG-046, CS-GCFG-047, CS-GCFG-052)", func() {
	var f *swapFixture
	BeforeEach(func() {
		f = newSwapFixture()
		write(f.target, cfg)
		Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
	})

	It("CS-GCFG-052: the link becomes a regular file and the linked file is parked under StateRoot", func() {
		Expect(globalcfg.Revert(f.opts())).To(Succeed(), f.errw.String())
		Expect(isLink(f.link)).To(BeFalse())
		Expect(read(f.link)).To(Equal(cfg))
		Expect(mode(f.link)).To(Equal(os.FileMode(0o600)))
		Expect(exists(f.target)).To(BeFalse(), "nothing stale left in the config dir")
		l := globalcfg.Classify(f.home, "")
		Expect(l.Mode).To(Equal(globalcfg.ModeLegacy))
		Expect(l.SplitBrain).To(BeFalse())
		parked := f.entries(globalcfg.RevertedPrefix)
		Expect(parked).To(HaveLen(1))
		Expect(read(parked[0])).To(Equal(cfg))
		Expect(f.out.String()).To(ContainSubstring("kept at " + parked[0]))
		Expect(leftoversIn(f.home)).To(BeEmpty())
	})

	It("CS-GCFG-052: the link must be the same file as the target (-ef)", func() {
		Expect(globalcfg.SameFile(f.link, f.target)).To(Succeed())
		other := filepath.Join(f.home, "other.json")
		write(other, cfg)
		Expect(globalcfg.SameFile(other, f.target)).To(MatchError(ContainSubstring("not the same file")))
		Expect(globalcfg.SameFile(filepath.Join(f.home, "missing"), f.target)).NotTo(Succeed())
	})

	It("CS-GCFG-052: an already legacy layout is a no-op; other layouts refuse and change nothing", func() {
		Expect(os.Remove(f.link)).To(Succeed())
		Expect(os.Rename(f.target, f.link)).To(Succeed())
		Expect(globalcfg.Revert(f.opts())).To(Succeed())
		Expect(f.out.String()).To(ContainSubstring("already a regular file"))

		write(f.target, "{}") // split brain
		expectRefused(globalcfg.Revert(f.opts()), "split brain")
		Expect(read(f.link)).To(Equal(cfg))
		Expect(read(f.target)).To(Equal("{}"))

		Expect(os.Remove(f.link)).To(Succeed())
		Expect(os.Symlink("elsewhere.json", f.link)).To(Succeed())
		expectRefused(globalcfg.Revert(f.opts()), "not the linked layout")

		Expect(os.Remove(f.link)).To(Succeed())
		expectRefused(globalcfg.Revert(f.opts()), "does not exist")
		Expect(exists(f.state)).To(BeFalse())
	})

	It("CS-GCFG-046: a container whose CLAUDE_SANDBOX_GLOBAL_CONFIG names this target refuses, read in one inspect", func() {
		f.fake.On("docker ps", "cs-linked\x1f/proj\ncs-legacy\x1f/proj\ncs-empty\x1f\ncs-otherhome\x1f\n", nil)
		f.fake.On("docker inspect", strings.Join([]string{
			`/cs-linked` + "\x1f" + `["PATH=/usr/bin","CLAUDE_SANDBOX_GLOBAL_CONFIG=` + f.target + `"]`,
			`/cs-legacy` + "\x1f" + `["PATH=/usr/bin"]`,
			`/cs-empty` + "\x1f" + `["CLAUDE_SANDBOX_GLOBAL_CONFIG="]`,
			`/cs-otherhome` + "\x1f" + `["CLAUDE_SANDBOX_GLOBAL_CONFIG=/home/someone/.claude/.claude.json"]`,
		}, "\n")+"\n", nil)
		err := globalcfg.Revert(f.opts())
		expectRefused(err, "cs-linked")
		Expect(err.Error()).NotTo(ContainSubstring("cs-legacy"))
		Expect(err.Error()).NotTo(ContainSubstring("cs-empty"))
		Expect(err.Error()).NotTo(ContainSubstring("cs-otherhome"))
		var inspects []string
		for _, l := range f.fake.CommandLines() {
			if strings.HasPrefix(l, "docker inspect") {
				inspects = append(inspects, l)
			}
		}
		Expect(inspects).To(HaveLen(1))
		Expect(inspects[0]).To(HaveSuffix(" cs-linked cs-legacy cs-empty cs-otherhome"))
		Expect(isLink(f.link)).To(BeTrue())
	})

	It("CS-GCFG-046: the inspect's stdout is parsed even when it exits non-zero", func() {
		f.fake.On("docker ps", "cs-linked\x1f/proj\ncs-gone\x1f/proj\n", nil)
		f.fake.On("docker inspect", `/cs-linked`+"\x1f"+`["CLAUDE_SANDBOX_GLOBAL_CONFIG=`+f.target+`"]`+"\n", execx.Fail(1))
		expectRefused(globalcfg.Revert(f.opts()), "cs-linked")
	})

	It("CS-GCFG-046: migrate never inspects the environment", func() {
		Expect(os.Remove(f.link)).To(Succeed())
		Expect(os.Rename(f.target, f.link)).To(Succeed())
		f.fake.On("docker ps", "cs-a\x1f/proj\n", nil)
		Expect(globalcfg.Migrate(f.opts())).To(Succeed())
		for _, l := range f.fake.CommandLines() {
			Expect(l).NotTo(HavePrefix("docker inspect"))
		}
	})

	It("CS-GCFG-047: revert --force warns that linked containers' saves are dropped or re-create the file", func() {
		f.fake.On("docker ps", "cs-linked\x1f/proj\n", nil)
		f.fake.On("docker inspect", `/cs-linked`+"\x1f"+`["CLAUDE_SANDBOX_GLOBAL_CONFIG=`+f.target+`"]`+"\n", nil)
		o := f.opts()
		o.Force = true
		Expect(globalcfg.Revert(o)).To(Succeed())
		w := f.errw.String()
		Expect(w).To(ContainSubstring("WARNING: --force"))
		Expect(w).To(ContainSubstring("cs-linked"))
		Expect(w).To(ContainSubstring("their saves are dropped, or re-create " + f.target + " as a defaults-based file"))
		Expect(w).To(ContainSubstring("split-brain warning"))
		Expect(w).To(ContainSubstring("Stop them and relaunch"))
		Expect(isLink(f.link)).To(BeFalse())
	})
})

var _ = Describe("the command guards (CS-GCFG-044, CS-GCFG-053)", func() {
	var f *swapFixture
	BeforeEach(func() {
		f = newSwapFixture()
		write(f.link, cfg)
	})

	It("CS-GCFG-044: migrate, revert and accept refuse inside a sandbox before anything else", func() {
		o := f.opts()
		o.InSandbox = true
		expectRefused(globalcfg.Migrate(o), "inside a sandbox")
		expectRefused(globalcfg.Revert(o), "inside a sandbox")
		expectRefused(globalcfg.Accept(globalcfg.AcceptOptions{Home: f.home, StateRoot: f.state, InSandbox: true}), "inside a sandbox")
		Expect(f.fake.CommandLines()).To(BeEmpty())
		Expect(exists(f.state)).To(BeFalse())
		Expect(read(f.link)).To(Equal(cfg))
	})

	It("CS-GCFG-053: the store dirs are 0700, the copies 0600, and the caps hold", func() {
		for i := 0; i < 5; i++ {
			Expect(globalcfg.Migrate(f.opts())).To(Succeed(), f.errw.String())
			Expect(globalcfg.Revert(f.opts())).To(Succeed(), f.errw.String())
			time.Sleep(2 * time.Millisecond) // distinct millisecond names
		}
		Expect(f.entries(globalcfg.PreMigratePrefix)).To(HaveLen(globalcfg.KeepPreMigrate))
		Expect(f.entries(globalcfg.RevertedPrefix)).To(HaveLen(globalcfg.KeepReverted))
		for _, d := range []string{f.state, filepath.Join(f.state, globalcfg.StoreDirName), f.storeDir()} {
			Expect(mode(d)).To(Equal(os.FileMode(0o700)), d)
		}
		for _, p := range append(f.entries(globalcfg.PreMigratePrefix), f.entries(globalcfg.RevertedPrefix)...) {
			Expect(mode(p)).To(Equal(os.FileMode(0o600)), p)
		}
		ents, _ := os.ReadDir(f.storeDir())
		for _, e := range ents {
			Expect(e.Name()).NotTo(HavePrefix(".tmp-"), "no temp file left")
		}
	})

	It("CS-GCFG-053: a symlinked store directory refuses before anything is written", func() {
		Expect(os.MkdirAll(filepath.Join(f.state, globalcfg.StoreDirName), 0o700)).To(Succeed())
		elsewhere := filepath.Join(filepath.Dir(f.state), "elsewhere")
		Expect(os.Mkdir(elsewhere, 0o700)).To(Succeed())
		Expect(os.Symlink(elsewhere, f.storeDir())).To(Succeed())
		expectRefused(globalcfg.Migrate(f.opts()), "symlink")
		Expect(read(f.link)).To(Equal(cfg))
		Expect(exists(f.target)).To(BeFalse())
		Expect(exists(f.link + ".lock")).To(BeFalse())
	})

	It("CS-GCFG-053: under go test the real home and the real state root panic before anything is written", func() {
		realHome, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		o := f.opts()
		o.Home = realHome
		Expect(func() { globalcfg.Migrate(o) }).To(PanicWith(ContainSubstring("real home")))
		Expect(func() { globalcfg.Revert(o) }).To(PanicWith(ContainSubstring("real home")))
		Expect(func() { globalcfg.Accept(globalcfg.AcceptOptions{Home: realHome, StateRoot: f.state}) }).To(PanicWith(ContainSubstring("real home")))
		realState := filepath.Join(realHome, ".local", "state", "claude-sandbox")
		Expect(func() { globalcfg.OpenStore(realState, f.link, nil) }).To(PanicWith(ContainSubstring("real state root")))
	})
})

var _ = Describe("global-config accept (CS-GCFG-054)", func() {
	var f *swapFixture
	var env map[string]string
	BeforeEach(func() {
		f = newSwapFixture()
		env = map[string]string{}
	})
	accept := func() error {
		return globalcfg.Accept(globalcfg.AcceptOptions{
			Home: f.home, StateRoot: f.state, Out: f.out,
			Getenv: func(k string) string { return env[k] },
		})
	}

	It("CS-GCFG-054: the first accept records a snapshot and prints key names and counts, never a value", func() {
		write(f.link, cfg)
		Expect(accept()).To(Succeed())
		snaps := f.entries(globalcfg.SnapshotPrefix)
		Expect(snaps).To(HaveLen(1))
		Expect(read(snaps[0])).To(Equal(cfg))
		Expect(mode(snaps[0])).To(Equal(os.FileMode(0o600)))
		out := f.out.String()
		Expect(out).To(ContainSubstring("Accepted " + f.link + " as the global-config baseline: " + snaps[0]))
		Expect(out).To(ContainSubstring("(no earlier snapshot)"))
		Expect(out).To(ContainSubstring("keys: 4\n"))
		Expect(out).To(ContainSubstring("projects: 2\n"))
		Expect(out).To(ContainSubstring("oauthAccount: present\n"))
		Expect(out).To(ContainSubstring("hasCompletedOnboarding: true\n"))
		Expect(out).NotTo(ContainSubstring("secret-value"))
		Expect(out).NotTo(ContainSubstring("918273"))
	})

	It("CS-GCFG-054: a later accept compares with the previous snapshot; the newest 5 are kept", func() {
		write(f.link, cfg)
		Expect(accept()).To(Succeed())
		time.Sleep(2 * time.Millisecond)
		write(f.link, `{"projects":{"a":{}},"hasCompletedOnboarding":false,"numStartups":8,"newKey":1}`)
		f.out.Reset()
		Expect(accept()).To(Succeed())
		out := f.out.String()
		Expect(out).To(ContainSubstring("keys: 4 (was 4); added: newKey; removed: oauthAccount\n"))
		Expect(out).To(ContainSubstring("projects: 1 (was 2)\n"))
		Expect(out).To(ContainSubstring("oauthAccount: absent (was present)\n"))
		Expect(out).To(ContainSubstring("hasCompletedOnboarding: false (was true)\n"))
		for i := 0; i < 6; i++ {
			time.Sleep(2 * time.Millisecond)
			Expect(accept()).To(Succeed())
		}
		Expect(f.entries(globalcfg.SnapshotPrefix)).To(HaveLen(globalcfg.KeepSnapshots))
	})

	It("CS-GCFG-054: the file is read through the link and keyed by its lexical path", func() {
		write(f.target, cfg)
		Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
		Expect(accept()).To(Succeed())
		Expect(f.entries(globalcfg.SnapshotPrefix)).To(HaveLen(1), "under StoreKey($HOME/.claude.json)")
	})

	It("CS-GCFG-054: CLAUDE_CONFIG_DIR and .config.json resolve as Claude Code does", func() {
		cd := filepath.Join(filepath.Dir(f.home), "work-claude")
		write(filepath.Join(cd, ".claude.json"), cfg)
		env["CLAUDE_CONFIG_DIR"] = cd
		Expect(accept()).To(Succeed())
		Expect(f.out.String()).To(ContainSubstring("Accepted " + filepath.Join(cd, ".claude.json")))
		Expect(exists(filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(filepath.Join(cd, ".claude.json"))))).To(BeTrue())

		write(filepath.Join(cd, ".config.json"), cfg)
		f.out.Reset()
		Expect(accept()).To(Succeed())
		Expect(f.out.String()).To(ContainSubstring("Accepted " + filepath.Join(cd, ".config.json")))

		delete(env, "CLAUDE_CONFIG_DIR")
		write(filepath.Join(f.home, ".claude", ".config.json"), cfg)
		p, err := globalcfg.ResolveGlobalFile(f.home, func(k string) string { return env[k] })
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(filepath.Join(f.home, ".claude", ".config.json")))
	})

	It("CS-GCFG-054: a relative or ~ CLAUDE_CONFIG_DIR, a custom OAuth URL, a missing or non-object file refuse", func() {
		env["CLAUDE_CONFIG_DIR"] = "rel/claude"
		expectRefused(accept(), "not a plain absolute path")
		env["CLAUDE_CONFIG_DIR"] = "/x/~/claude"
		expectRefused(accept(), "not a plain absolute path")
		delete(env, "CLAUDE_CONFIG_DIR")
		env[globalcfg.CustomOAuthEnv] = "https://example.invalid"
		expectRefused(accept(), globalcfg.CustomOAuthEnv)
		delete(env, globalcfg.CustomOAuthEnv)
		expectRefused(accept(), "no such file")
		write(f.link, `[1,2]`)
		expectRefused(accept(), "JSON object")
		write(f.link, `{"a":`)
		expectRefused(accept(), "JSON object")
		Expect(exists(f.state)).To(BeFalse())
	})
})
