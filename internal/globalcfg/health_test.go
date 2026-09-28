package globalcfg_test

// Spec: spec/global-config.feature CS-GCFG-001..015 — the launcher's health
// check. Every test uses a scratch HOME and a scratch state root under
// GinkgoT().TempDir(), a fake clock and a recording sleep; nothing touches
// the real home, ~/.claude.json or the real state root.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/globalcfg"
	"github.com/kmacmcfarlane/claude-sandbox/internal/hostdirs"
)

type healthFixture struct {
	home, state, link, target string
	env                       map[string]string
	errw                      *bytes.Buffer
	now                       time.Time
	slept                     []time.Duration
	reads                     int
	readFile                  func(string) ([]byte, error)
	prev                      *globalcfg.Health
}

func newHealthFixture() *healthFixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &healthFixture{
		home:  filepath.Join(base, "home"),
		state: filepath.Join(base, "state"),
		env:   map[string]string{},
		errw:  &bytes.Buffer{},
		now:   time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
	}
	Expect(os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700)).To(Succeed())
	f.link = filepath.Join(f.home, ".claude.json")
	f.target = filepath.Join(f.home, ".claude", ".claude.json")
	return f
}

func (f *healthFixture) check() *globalcfg.Health {
	return globalcfg.CheckHealth(globalcfg.HealthOptions{
		Home: f.home, StateRoot: f.state,
		Getenv: func(k string) string { return f.env[k] },
		Err:    f.errw,
		Now:    func() time.Time { return f.now },
		Sleep:  func(d time.Duration) { f.slept = append(f.slept, d) },
		ReadFile: func(p string) ([]byte, error) {
			f.reads++
			if f.readFile != nil {
				return f.readFile(p)
			}
			return os.ReadFile(p)
		},
		Previous: f.prev,
	})
}

func (f *healthFixture) snapshots(file string) []string {
	s := globalcfg.Store{Dir: filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(file))}
	return s.List(globalcfg.SnapshotPrefix)
}

// healthy is cfg plus a firstStartTime; values that must never be printed
// are "secret-value", "918273" and the timestamp.
const healthy = `{"projects":{"a":{},"b":{},"c":{},"d":{}},"oauthAccount":{"emailAddress":"secret-value"},"hasCompletedOnboarding":true,"numStartups":918273,"firstStartTime":"2025-01-02T03:04:05.678Z"}`

var _ = Describe("global-config health check (CS-GCFG-001..015)", func() {
	var f *healthFixture
	BeforeEach(func() { f = newHealthFixture() })

	baseline := func() {
		write(f.link, healthy)
		h := f.check()
		Expect(h.Snapshot).NotTo(BeEmpty())
		Expect(f.errw.String()).To(BeEmpty())
	}

	Describe("CS-GCFG-002: the file and when the check stands aside", func() {
		It("CS-GCFG-002: a custom OAuth URL, a relative or ~ CLAUDE_CONFIG_DIR, a relative HOME: nothing read, printed or written", func() {
			home := f.home
			write(f.link, `{"a":`)
			for _, set := range []func(){
				func() { f.env[globalcfg.CustomOAuthEnv] = "https://example.invalid" },
				func() { f.env["CLAUDE_CONFIG_DIR"] = "rel/claude" },
				func() { f.env["CLAUDE_CONFIG_DIR"] = "/x/~/claude" },
				func() { f.home = "rel/home" },
			} {
				f.env, f.home, f.reads = map[string]string{}, home, 0
				set()
				h := f.check()
				Expect(h.File).To(BeEmpty())
				Expect(f.reads).To(BeZero())
				Expect(f.errw.String()).To(BeEmpty())
				Expect(exists(f.state)).To(BeFalse())
			}
		})

		It("CS-GCFG-002: the default file, $CLAUDE_CONFIG_DIR/.claude.json and .config.json are the ones judged", func() {
			write(f.link, healthy)
			Expect(f.check().File).To(Equal(f.link))
			cd := filepath.Join(filepath.Dir(f.home), "work-claude")
			write(filepath.Join(cd, ".claude.json"), healthy)
			f.env["CLAUDE_CONFIG_DIR"] = cd
			Expect(f.check().File).To(Equal(filepath.Join(cd, ".claude.json")))
			write(filepath.Join(cd, ".config.json"), healthy)
			Expect(f.check().File).To(Equal(filepath.Join(cd, ".config.json")))
		})
	})

	Describe("CS-GCFG-003: snapshots per file, by lexical path", func() {
		It("CS-GCFG-003: alternating the default layout and a CLAUDE_CONFIG_DIR tree warns about neither", func() {
			write(f.link, healthy)
			cd := filepath.Join(filepath.Dir(f.home), "work-claude")
			write(filepath.Join(cd, ".claude.json"), `{"projects":{},"numStartups":1}`)
			for i := 0; i < 3; i++ {
				delete(f.env, "CLAUDE_CONFIG_DIR")
				Expect(f.check().Damaged()).To(BeFalse())
				f.env["CLAUDE_CONFIG_DIR"] = cd
				Expect(f.check().Damaged()).To(BeFalse())
			}
			Expect(f.errw.String()).To(BeEmpty())
			Expect(f.snapshots(f.link)).To(HaveLen(1))
			Expect(f.snapshots(filepath.Join(cd, ".claude.json"))).To(HaveLen(1))
		})

		It("CS-GCFG-003: the baseline survives a migrate and a host mv over the link", func() {
			baseline()
			// migrate by hand, then damage the linked file
			Expect(os.Rename(f.link, f.target)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			Expect(f.check().Damaged()).To(BeFalse())
			write(f.target, `{"numStartups":1}`)
			Expect(f.check().Damaged()).To(BeTrue(), "compared with the pre-migration baseline")
			// a host "mv" over the link: a regular file again
			write(f.link+".tmp", `{"numStartups":1}`)
			Expect(os.Rename(f.link+".tmp", f.link)).To(Succeed())
			Expect(f.check().Damaged()).To(BeTrue())
			Expect(f.snapshots(f.link)).To(HaveLen(1))
		})
	})

	Describe("CS-GCFG-004: the parse retry", func() {
		It("CS-GCFG-004: a file that parses at once is read once, with no wait", func() {
			write(f.link, healthy)
			f.check()
			Expect(f.reads).To(Equal(1))
			Expect(f.slept).To(BeEmpty())
		})

		It("CS-GCFG-004: two torn reads, then a good one: judged healthy after waits of 150 and 200 ms", func() {
			baseline()
			f.reads = 0
			f.readFile = func(p string) ([]byte, error) {
				if f.reads < 3 {
					return []byte(healthy[:f.reads*10]), nil // 0 bytes, then a prefix
				}
				return os.ReadFile(p)
			}
			Expect(f.check().Damaged()).To(BeFalse())
			Expect(f.reads).To(Equal(3))
			Expect(f.slept).To(Equal([]time.Duration{150 * time.Millisecond, 200 * time.Millisecond}))
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-GCFG-004: a third failure counts as does-not-parse", func() {
			baseline()
			f.reads = 0
			write(f.link, "")
			h := f.check()
			Expect(f.reads).To(Equal(3))
			Expect(h.Findings).To(Equal([]string{"it does not parse as a JSON object (3 reads)"}))
		})
	})

	Describe("CS-GCFG-005/006: snapshots", func() {
		It("CS-GCFG-005: the first healthy read is written as the baseline, 0600, silently", func() {
			write(f.link, healthy)
			h := f.check()
			snaps := f.snapshots(f.link)
			Expect(snaps).To(HaveLen(1))
			Expect(h.Snapshot).To(Equal(snaps[0]))
			Expect(read(snaps[0])).To(Equal(healthy))
			Expect(mode(snaps[0])).To(Equal(os.FileMode(0o600)))
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-GCFG-006: under an hour nothing is written, even when the content changed; at an hour one is; 5 are kept", func() {
			baseline()
			f.now = f.now.Add(59 * time.Minute)
			write(f.link, strings.Replace(healthy, "918273", "918274", 1))
			Expect(f.check().Snapshot).To(BeEmpty())
			Expect(f.snapshots(f.link)).To(HaveLen(1))
			f.now = f.now.Add(time.Minute)
			Expect(f.check().Snapshot).NotTo(BeEmpty())
			Expect(f.snapshots(f.link)).To(HaveLen(2))
			for i := 0; i < 6; i++ {
				f.now = f.now.Add(time.Hour)
				f.check()
			}
			Expect(f.snapshots(f.link)).To(HaveLen(globalcfg.KeepSnapshots))
			Expect(f.errw.String()).To(BeEmpty())
		})

		It("CS-GCFG-006: while another launch holds the snapshot lock, the snapshot is skipped silently, the check is not", func() {
			write(f.link, healthy)
			store, err := globalcfg.OpenStore(f.state, f.link, nil)
			Expect(err).NotTo(HaveOccurred())
			unlock, ok := store.TryLock()
			Expect(ok).To(BeTrue())
			h := f.check()
			Expect(h.File).To(Equal(f.link))
			Expect(h.Snapshot).To(BeEmpty())
			Expect(f.snapshots(f.link)).To(BeEmpty())
			Expect(f.errw.String()).To(BeEmpty())
			unlock()
			Expect(f.check().Snapshot).NotTo(BeEmpty())
		})

		It("CS-GCFG-006: a burst of healthy checks past the hour writes one snapshot", func() {
			baseline()
			f.now = f.now.Add(2 * time.Hour)
			var wg sync.WaitGroup
			var mu sync.Mutex
			var outs []string
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer GinkgoRecover()
					defer wg.Done()
					var errw bytes.Buffer
					globalcfg.CheckHealth(globalcfg.HealthOptions{
						Home: f.home, StateRoot: f.state, Err: &errw,
						Getenv: func(string) string { return "" },
						// distinct milliseconds, so no two names collide
						Now: func() time.Time { return f.now.Add(time.Duration(i) * time.Millisecond) },
					})
					mu.Lock()
					outs = append(outs, errw.String())
					mu.Unlock()
				}(i)
			}
			wg.Wait()
			Expect(f.snapshots(f.link)).To(HaveLen(2), "one new snapshot for the burst")
			Expect(strings.Join(outs, "")).To(BeEmpty())
		})

		It("CS-GCFG-006: .tmp-* files older than an hour are removed when the store is opened; newer ones stay", func() {
			dir := filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(f.link))
			old, fresh := filepath.Join(dir, ".tmp-old"), filepath.Join(dir, ".tmp-fresh")
			write(old, "x")
			write(fresh, "x")
			Expect(os.Chtimes(old, time.Now(), time.Now().Add(-2*time.Hour))).To(Succeed())
			_, err := globalcfg.OpenStore(f.state, f.link, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(exists(old)).To(BeFalse())
			Expect(exists(fresh)).To(BeTrue())
		})

		It("CS-GCFG-006: a snapshot name without a time is aged by its mtime", func() {
			write(f.link, healthy)
			dir := filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(f.link))
			manual := filepath.Join(dir, globalcfg.SnapshotPrefix+"manual")
			write(manual, healthy)
			Expect(os.Chtimes(manual, f.now, f.now.Add(-10*time.Minute))).To(Succeed())
			Expect(f.check().Snapshot).To(BeEmpty())
			Expect(os.Chtimes(manual, f.now, f.now.Add(-2*time.Hour))).To(Succeed())
			Expect(f.check().Snapshot).NotTo(BeEmpty())
		})
	})

	Describe("CS-GCFG-007: what counts as damage", func() {
		DescribeTable("CS-GCFG-007: findings against the baseline",
			func(cur string, want []string) {
				baseline()
				write(f.link, cur)
				Expect(f.check().Findings).To(Equal(want))
			},
			Entry("CS-GCFG-007: oauthAccount lost", strings.Replace(healthy, `"oauthAccount":{"emailAddress":"secret-value"},`, "", 1), []string{"oauthAccount is gone"}),
			Entry("CS-GCFG-007: oauthAccount null", strings.Replace(healthy, `{"emailAddress":"secret-value"}`, "null", 1), []string{"oauthAccount is gone"}),
			Entry("CS-GCFG-007: onboarding lost", strings.Replace(healthy, `"hasCompletedOnboarding":true`, `"hasCompletedOnboarding":false`, 1), []string{"hasCompletedOnboarding is no longer true"}),
			Entry("CS-GCFG-007: projects below half", strings.Replace(healthy, `{"a":{},"b":{},"c":{},"d":{}}`, `{"a":{}}`, 1), []string{"projects fell from 4 to 1"}),
			Entry("CS-GCFG-007: firstStartTime changed", strings.Replace(healthy, "2025-01-02", "2026-09-26", 1), []string{"firstStartTime changed"}),
			Entry("CS-GCFG-007: firstStartTime gone", strings.Replace(healthy, `,"firstStartTime":"2025-01-02T03:04:05.678Z"`, "", 1), []string{"firstStartTime is gone"}),
			Entry("CS-GCFG-007: a defaults file loses all of it", `{"userID":"x","migrationVersion":3,"firstStartTime":"2026-09-26T17:28:08.683Z"}`,
				[]string{"oauthAccount is gone", "hasCompletedOnboarding is no longer true", "projects fell from 4 to 0", "firstStartTime changed"}),
			Entry("CS-GCFG-007: a JSON array does not parse as an object", `[1]`, []string{"it does not parse as a JSON object (3 reads)"}),
			Entry("CS-GCFG-007: new keys, numStartups, more projects: healthy", strings.Replace(strings.Replace(healthy, "918273", "1", 1), `"d":{}}`, `"d":{},"e":{}},"newKey":1`, 1), []string(nil)),
			Entry("CS-GCFG-007: half the projects is still healthy", strings.Replace(healthy, `{"a":{},"b":{},"c":{},"d":{}}`, `{"a":{},"b":{}}`, 1), []string(nil)),
		)

		It("CS-GCFG-007: a newer snapshot that does not parse is skipped, as baseline and as restore source", func() {
			baseline()
			good := f.snapshots(f.link)[0]
			dir := filepath.Dir(good)
			bad := filepath.Join(dir, globalcfg.SnapshotPrefix+"9999999999999")
			write(bad, `{"torn`)
			write(f.link, `{}`)
			h := f.check()
			Expect(h.Findings).To(ContainElement("oauthAccount is gone"))
			Expect(f.errw.String()).To(ContainSubstring("Last good snapshot: " + good))
			Expect(f.errw.String()).NotTo(ContainSubstring(bad))
		})

		It("CS-GCFG-007: a missing file with a baseline is damage", func() {
			baseline()
			Expect(os.Remove(f.link)).To(Succeed())
			Expect(f.check().Findings).To(Equal([]string{"it is missing"}))
		})
	})

	Describe("CS-GCFG-008..011: the warning and the restore command", func() {
		It("CS-GCFG-008: one warning with key names and counts, the snapshot and its time, accept; nothing written; it repeats", func() {
			baseline()
			snap := f.snapshots(f.link)[0]
			damaged := `{"userID":"x","numStartups":918273}`
			write(f.link, damaged)
			f.now = f.now.Add(3 * time.Hour)
			h := f.check()
			w := f.errw.String()
			Expect(strings.Count(w, "WARNING")).To(Equal(1))
			Expect(w).To(ContainSubstring("WARNING: the global config " + f.link + " looks damaged: oauthAccount is gone; hasCompletedOnboarding is no longer true; projects fell from 4 to 0; firstStartTime is gone."))
			Expect(w).To(ContainSubstring("Last good snapshot: " + snap + " (taken 2026-09-28T12:00:00Z, 3h0m0s ago). Nothing was changed."))
			Expect(w).To(ContainSubstring(globalcfg.AcceptCmd))
			for _, v := range []string{"secret-value", "918273", "2025-01-02", `"x"`} {
				Expect(w).NotTo(ContainSubstring(v))
			}
			Expect(h.Snapshot).To(BeEmpty())
			Expect(f.snapshots(f.link)).To(HaveLen(1), "no snapshot while damaged, even past the hour")
			Expect(read(f.link)).To(Equal(damaged))
			f.errw.Reset()
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("looks damaged"), "repeats on the next launch")
		})

		It("CS-GCFG-009: linked layout: cp to .restore and mv -f in ~/.claude, the link left alone", func() {
			write(f.target, healthy)
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			f.check()
			snap := f.snapshots(f.link)[0]
			write(f.target, `{}`)
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("rename it into place"))
			Expect(f.errw.String()).To(ContainSubstring("    cp " + snap + " " + f.target + ".restore && mv -f " + f.target + ".restore " + f.target + "\n"))
		})

		It("CS-GCFG-009: a dangling link that names ~/.claude/.claude.json gets the linked command", func() {
			baseline()
			snap := f.snapshots(f.link)[0]
			Expect(os.Remove(f.link)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("it is missing"))
			Expect(f.errw.String()).To(ContainSubstring("cp " + snap + " " + f.target + ".restore && mv -f"))
		})

		It("CS-GCFG-010: legacy layout: exit every session first, then cp in place", func() {
			baseline()
			snap := f.snapshots(f.link)[0]
			write(f.link, `{}`)
			f.check()
			w := f.errw.String()
			Expect(w).To(ContainSubstring("first exit every Claude session, host and sandboxes (tmux kill-server does not stop a sandbox)"))
			Expect(w).To(ContainSubstring("cp keeps the inode"))
			Expect(w).To(ContainSubstring("    cp " + snap + " " + f.link + "\n"))
			Expect(w).NotTo(ContainSubstring("mv -f"))
		})

		It("CS-GCFG-010: a deleted link of a migrated layout gets ln -s, never a cp over the live file", func() {
			write(f.target, healthy)
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			f.check()
			Expect(os.Remove(f.link)).To(Succeed())
			f.check()
			w := f.errw.String()
			Expect(w).To(ContainSubstring("it is missing"))
			Expect(w).To(ContainSubstring("the link is gone: " + f.target + " is the live file"))
			Expect(w).To(ContainSubstring("    ln -s .claude/.claude.json " + f.link + "\n"))
			Expect(w).NotTo(ContainSubstring("    cp "))
		})

		It("CS-GCFG-010: a missing legacy file gets the cp without the inode reason", func() {
			baseline()
			snap := f.snapshots(f.link)[0]
			Expect(os.Remove(f.link)).To(Succeed())
			f.check()
			w := f.errw.String()
			Expect(w).To(ContainSubstring("first exit every Claude session"))
			Expect(w).To(ContainSubstring("still holds the deleted file"))
			Expect(w).NotTo(ContainSubstring("keeps the inode"))
			Expect(w).To(ContainSubstring("    cp " + snap + " " + f.link + "\n"))
		})

		It("CS-GCFG-010: paths that need it are shell-quoted", func() {
			Expect(globalcfg.ShellQuote("/home/a b/it's")).To(Equal(`'/home/a b/it'\''s'`))
			Expect(globalcfg.ShellQuote("/home/rt/.claude.json")).To(Equal("/home/rt/.claude.json"))
		})

		It("CS-GCFG-011: CLAUDE_CONFIG_DIR: .restore and mv -f in that dir; a symlinked file gets the cp form", func() {
			cd := filepath.Join(filepath.Dir(f.home), "work-claude")
			file := filepath.Join(cd, ".claude.json")
			f.env["CLAUDE_CONFIG_DIR"] = cd
			write(file, healthy)
			f.check()
			snap := f.snapshots(file)[0]
			write(file, `{}`)
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("cp " + snap + " " + file + ".restore && mv -f " + file + ".restore " + file + "\n"))

			f.errw.Reset()
			real := filepath.Join(cd, "real.json")
			write(real, `{}`)
			Expect(os.Remove(file)).To(Succeed())
			Expect(os.Symlink("real.json", file)).To(Succeed())
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("    cp " + snap + " " + file + "\n"))
			Expect(f.errw.String()).To(ContainSubstring("first exit every Claude session"))
		})

		It("CS-GCFG-011: a refused link elsewhere gets no command, only fix-the-layout-first", func() {
			baseline()
			other := filepath.Join(f.home, "other.json")
			write(other, `{}`)
			Expect(os.Remove(f.link)).To(Succeed())
			Expect(os.Symlink("other.json", f.link)).To(Succeed())
			f.check()
			w := f.errw.String()
			Expect(w).To(ContainSubstring("first fix the layout of " + f.link + " (it must name " + f.target))
			Expect(w).NotTo(ContainSubstring("    cp "))
		})

		It("CS-GCFG-011: a link to ~/.claude/.claude.json that is a directory gets no command, and the problem is named", func() {
			baseline()
			Expect(os.Remove(f.link)).To(Succeed())
			Expect(os.MkdirAll(f.target, 0o700)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", f.link)).To(Succeed())
			f.check()
			w := f.errw.String()
			Expect(w).To(ContainSubstring("first fix the layout of " + f.link + " (the target is not a regular file (a directory))"))
			Expect(w).NotTo(ContainSubstring("mv -f"))
		})
	})

	It("CS-GCFG-012: after accept the next check is healthy against the new baseline", func() {
		baseline()
		loggedOut := `{"projects":{"a":{},"b":{},"c":{},"d":{}},"hasCompletedOnboarding":true,"firstStartTime":"2025-01-02T03:04:05.678Z"}`
		write(f.link, loggedOut)
		Expect(f.check().Damaged()).To(BeTrue())
		f.now = f.now.Add(time.Minute)
		Expect(globalcfg.Accept(globalcfg.AcceptOptions{
			Home: f.home, StateRoot: f.state, Getenv: func(k string) string { return f.env[k] },
			Now: func() time.Time { return f.now },
		})).To(Succeed())
		f.errw.Reset()
		f.now = f.now.Add(time.Minute)
		Expect(f.check().Damaged()).To(BeFalse())
		Expect(f.errw.String()).To(BeEmpty())
	})

	Describe("CS-GCFG-013: no baseline", func() {
		It("CS-GCFG-013: an unparseable file warns, names Claude Code's backups, writes nothing", func() {
			write(f.link, `{"a":`)
			h := f.check()
			Expect(h.Damaged()).To(BeTrue())
			w := f.errw.String()
			Expect(w).To(ContainSubstring("WARNING: the global config " + f.link + ": it does not parse as a JSON object (3 reads), and there is no snapshot of it to restore from. Claude Code keeps copies in " + filepath.Join(f.home, ".claude", "backups") + "/. Nothing was changed."))
			Expect(f.snapshots(f.link)).To(BeEmpty())
		})

		It("CS-GCFG-013: a file that cannot be read (not ENOENT) warns too, after the retries", func() {
			f.readFile = func(string) ([]byte, error) {
				return nil, &os.PathError{Op: "open", Path: f.link, Err: syscall.EACCES}
			}
			h := f.check()
			Expect(f.reads).To(Equal(3))
			Expect(h.Findings).To(Equal([]string{"it cannot be read (permission denied)"}))
			Expect(f.errw.String()).To(ContainSubstring("it cannot be read (permission denied), and there is no snapshot"))
		})

		It("CS-GCFG-013: a missing file is a first run: silent", func() {
			h := f.check()
			Expect(h.Damaged()).To(BeFalse())
			Expect(f.errw.String()).To(BeEmpty())
			Expect(f.snapshots(f.link)).To(BeEmpty())
		})
	})

	Describe("CS-GCFG-014: the check after the session", func() {
		It("CS-GCFG-014: the same findings are not repeated; different ones are", func() {
			baseline()
			write(f.link, `{"projects":{"a":{},"b":{},"c":{},"d":{}},"hasCompletedOnboarding":true,"firstStartTime":"2025-01-02T03:04:05.678Z"}`)
			f.prev = f.check()
			Expect(f.prev.Warned).To(BeTrue())
			f.errw.Reset()
			f.check()
			Expect(f.errw.String()).To(BeEmpty())
			write(f.link, `{}`)
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("looks damaged"))
		})

		It("CS-GCFG-014: a healthy check before the session does not hide damage after it", func() {
			baseline()
			f.prev = f.check()
			write(f.link, `{}`)
			f.check()
			Expect(f.errw.String()).To(ContainSubstring("looks damaged"))
		})
	})

	Describe("CS-GCFG-015: never fatal, never the real files", func() {
		It("CS-GCFG-015: a symlinked state dir is one warning, and the check returns", func() {
			write(f.link, healthy)
			elsewhere := filepath.Join(filepath.Dir(f.state), "elsewhere")
			Expect(os.MkdirAll(elsewhere, 0o700)).To(Succeed())
			Expect(os.Symlink(elsewhere, f.state)).To(Succeed())
			h := f.check()
			Expect(h.Snapshot).To(BeEmpty())
			Expect(strings.Count(f.errw.String(), "WARNING")).To(Equal(1))
			Expect(f.errw.String()).To(ContainSubstring("global-config health check skipped: the state directory"))
		})

		It("CS-GCFG-015: a snapshot that cannot be written is one warning", func() {
			write(f.link, healthy)
			dir := filepath.Join(f.state, globalcfg.StoreDirName, globalcfg.StoreKey(f.link))
			Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
			// The lock file exists, so the lock is taken; the write then fails.
			write(filepath.Join(dir, globalcfg.SnapshotLockName), "")
			Expect(os.Chmod(dir, 0o500)).To(Succeed())
			DeferCleanup(func() { os.Chmod(dir, 0o700) })
			if os.Getuid() == 0 {
				Skip("root ignores the directory mode")
			}
			// EnsureOwnedDir re-modes it to 0700; make the write fail another way.
			ops := &hostdirs.Ops{Fchmod: func(*os.File, os.FileMode) error { return nil }}
			h := globalcfg.CheckHealth(globalcfg.HealthOptions{
				Home: f.home, StateRoot: f.state, Err: f.errw, DirOps: ops,
				Getenv: func(string) string { return "" },
			})
			Expect(h.Snapshot).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring("WARNING: global-config health check: writing a snapshot of " + f.link))
		})

		It("CS-GCFG-015: under go test the real home panics before anything is read", func() {
			real, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			Expect(func() {
				globalcfg.CheckHealth(globalcfg.HealthOptions{Home: real, StateRoot: f.state,
					ReadFile: func(string) ([]byte, error) { Fail("read the real home"); return nil, nil }})
			}).To(PanicWith(ContainSubstring("real home")))
		})

		It("CS-GCFG-015: under go test the real state root panics before anything is written", func() {
			real, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			write(f.link, healthy)
			Expect(func() {
				globalcfg.CheckHealth(globalcfg.HealthOptions{Home: f.home, StateRoot: hostdirs.StateRoot(real, nil)})
			}).To(PanicWith(ContainSubstring("real state root")))
		})
	})
})
