package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-064..068) — the resurrect hook forms of
// `tmux restore`: --pin (the keyed pin, preexisting, the sparse verdict within
// its deadline, the notice, the prunes), the pin a --resurrected restore
// reads, and --rearm (new panes only, matched by coordinates and saved dir,
// marked pending, retyped only where the saved full command was empty, the
// pin consumed, the deadline). Scratch dirs only; tmux is faked through
// execx.Fake.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("tmux restore: the resurrect hooks (CS-TMUX-064..068)", func() {
	var (
		dir    string
		cache  string
		proj   string
		fake   *execx.Fake
		logged []string
		now    = time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
		srv    = tmuxpane.Server{PID: 4242, Start: 1790000000}
	)

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		cache = GinkgoT().TempDir()
		var err error
		proj, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		fake = &execx.Fake{}
		logged = nil
		DeferCleanup(func(p, r time.Duration) { tmuxpane.PinDeadline, tmuxpane.RearmDeadline = p, r },
			tmuxpane.PinDeadline, tmuxpane.RearmDeadline)
	})

	opts := func() tmuxpane.HookOptions {
		return tmuxpane.HookOptions{Runner: fake, Dir: func() string { return dir }, CacheDir: cache,
			Now: func() time.Time { return now }, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	}
	stamp := func(min int) string { return now.Add(time.Duration(min) * time.Minute).Format("20060102T150405") }
	mark := func(i int, state string) tmuxpane.Mark {
		return tmuxpane.Mark{V: 1, State: state, Mode: tmuxpane.ModeClaude, Container: fmt.Sprintf("c%d", i),
			Project: proj, Conversation: fmt.Sprintf("%08d-1f2d-4e5f-8a9b-0c1d2e3f4a5b", i)}
	}
	// save writes a state file (one bare-shell pane line per row unless
	// lines is given) and a sidecar of n rows of the given server.
	save := func(st string, s *tmuxpane.Server, rows []tmuxpane.Row, lines string) {
		if lines == "" {
			for _, r := range rows {
				lines += fmt.Sprintf("pane\t%s\t%d\t1\t:*\t%d\tt\t:%s\t1\tzsh\t:\n", r.Session, r.Window, r.Pane, proj)
			}
		}
		Expect(os.WriteFile(filepath.Join(dir, tmuxpane.StateFileName(st)), []byte(lines), 0o600)).To(Succeed())
		Expect(tmuxpane.WriteSidecar(filepath.Join(dir, tmuxpane.SidecarName(st)), tmuxpane.Sidecar{
			V: 1, StateFile: tmuxpane.StateFileName(st), Server: s, Panes: rows})).To(Succeed())
	}
	rowsOf := func(n int, state string) []tmuxpane.Row {
		var out []tmuxpane.Row
		for i := 0; i < n; i++ {
			out = append(out, tmuxpane.Row{Session: "main", Window: i, Pane: 0, Mark: mark(i, state)})
		}
		return out
	}
	link := func(st string) {
		os.Remove(filepath.Join(dir, "last"))
		Expect(os.Symlink(tmuxpane.StateFileName(st), filepath.Join(dir, "last"))).To(Succeed())
	}
	pinFiles := func() []string {
		m, _ := filepath.Glob(filepath.Join(dir, "claude-sandbox-restore-pin.*"))
		return m
	}
	readPinFile := func(path string) map[string]any {
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var v map[string]any
		Expect(json.Unmarshal(b, &v)).To(Succeed())
		return v
	}

	Describe("CS-TMUX-064: --pin", func() {
		BeforeEach(func() {
			fake.On("tmux list-panes", "%0\t4242\t1790000000\n%1\t4242\t1790000000\n", nil)
		})

		It("CS-TMUX-064: pins last's save with the pre-existing panes and the server, 0600, before any verdict", func() {
			save(stamp(-1), &srv, rowsOf(2, tmuxpane.StateActive), "")
			link(stamp(-1))
			res := tmuxpane.PinRestore(opts())
			Expect(logged).To(BeEmpty())
			Expect(res.Path).To(Equal(filepath.Join(dir, tmuxpane.PinName(4242, now.UnixMilli()))))
			fi, err := os.Stat(res.Path)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
			v := readPinFile(res.Path)
			Expect(v).To(HaveKeyWithValue("v", BeNumerically("==", 1)))
			Expect(v).To(HaveKeyWithValue("serverPid", BeNumerically("==", 4242)))
			Expect(v).To(HaveKeyWithValue("stamp", stamp(-1)))
			Expect(v).To(HaveKeyWithValue("sidecar", tmuxpane.SidecarName(stamp(-1))))
			Expect(v).To(HaveKeyWithValue("stateFile", tmuxpane.StateFileName(stamp(-1))))
			Expect(v).To(HaveKeyWithValue("preexisting", []any{"%0", "%1"}))
			Expect(v).To(HaveKeyWithValue("sparse", HaveKeyWithValue("known", false)), "no earlier save: no baseline")
			Expect(fake.CommandLines()).To(Equal([]string{"tmux list-panes -a -F #{pane_id}\t#{pid}\t#{start_time}"}))
			Expect(res.Noticed).To(BeFalse())
			Expect(filepath.Join(cache, tmuxpane.NoticeFile)).NotTo(BeAnExistingFile())
		})

		It("CS-TMUX-064: a sparse save leaves the notice file and the tmux option, text without '#'", func() {
			other := tmuxpane.Server{PID: 100, Start: 1780000000}
			save(stamp(-60), &other, rowsOf(9, tmuxpane.StateActive), "")
			save(stamp(-1), &srv, rowsOf(1, tmuxpane.StateActive), "")
			link(stamp(-1))
			res := tmuxpane.PinRestore(opts())
			Expect(res.Pin.Sparse).NotTo(BeNil())
			Expect(*res.Pin.Sparse).To(Equal(tmuxpane.PinVerdict{N: 1, M: 9, K: 1, Lifetimes: true, Known: true, Sparse: true}))
			Expect(readPinFile(res.Path)).To(HaveKeyWithValue("sparse", HaveKeyWithValue("sparse", true)))
			Expect(res.Noticed).To(BeTrue())
			Expect(res.OptionSet).To(BeTrue())
			text := "sparse restore: 1 of 9 sandbox panes — claude-sandbox tmux restore --list"
			Expect(fake.CommandLines()).To(ContainElement("tmux set -g @claude-sandbox-notice " + text))
			Expect(text).NotTo(ContainSubstring("#"))
			out := &bytes.Buffer{}
			Expect(tmuxpane.PrintNotice(cache, now, out)).To(ContainSubstring("this save (" + stamp(-1) + ") has 1 sandbox panes"))
			Expect(out.String()).NotTo(BeEmpty())
			Expect(filepath.Join(cache, tmuxpane.NoticeFile)).To(BeAnExistingFile(), "printing never claims")
		})

		It("CS-TMUX-064: a verdict the deadline cuts short leaves sparse null, no notice, one log line", func() {
			tmuxpane.PinDeadline = 30 * time.Millisecond
			other := tmuxpane.Server{PID: 100, Start: 1780000000}
			save(stamp(-60), &other, rowsOf(9, tmuxpane.StateActive), "")
			save(stamp(-1), &srv, rowsOf(1, tmuxpane.StateActive), "")
			link(stamp(-1))
			fake = &execx.Fake{}
			fake.OnFunc("tmux list-panes", func(execx.Cmd) (string, error) {
				time.Sleep(50 * time.Millisecond)
				return "%0\t4242\t1790000000\n", nil
			})
			res := tmuxpane.PinRestore(opts())
			Expect(res.Path).NotTo(BeEmpty(), "the pin first")
			Expect(readPinFile(res.Path)).To(HaveKeyWithValue("sparse", BeNil()))
			Expect(res.Noticed).To(BeFalse())
			Expect(logged).To(ConsistOf(ContainSubstring("deadline: the sparse verdict")))
			Expect(fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set")))
		})

		It("CS-TMUX-064: no tmux answer, no server, or a last naming no save writes no pin and logs", func() {
			fake = &execx.Fake{}
			fake.On("tmux list-panes", "", execx.Fail(1))
			Expect(tmuxpane.PinRestore(opts()).Path).To(BeEmpty())
			fake = &execx.Fake{}
			fake.On("tmux list-panes", "%0\tx\ty\n", nil)
			Expect(tmuxpane.PinRestore(opts()).Path).To(BeEmpty())
			fake = &execx.Fake{}
			fake.On("tmux list-panes", "%0\t4242\t1790000000\n", nil)
			Expect(tmuxpane.PinRestore(opts()).Path).To(BeEmpty())
			Expect(pinFiles()).To(BeEmpty())
			Expect(logged).To(HaveLen(3))
			Expect(logged[0]).To(ContainSubstring("list-panes failed"))
			Expect(logged[1]).To(ContainSubstring("no pid and start time"))
			Expect(logged[2]).To(ContainSubstring("names no save"))
		})

		It("CS-TMUX-064: pins older than a day are pruned, consumed or not; nothing else is touched", func() {
			save(stamp(-1), &srv, rowsOf(1, tmuxpane.StateActive), "")
			link(stamp(-1))
			old := now.Add(-25 * time.Hour).UnixMilli()
			young := now.Add(-time.Hour).UnixMilli()
			names := []string{
				tmuxpane.PinName(1, old),
				strings.TrimSuffix(tmuxpane.PinName(2, old), ".json") + ".consumed.json",
				tmuxpane.PinName(3, young),
				"claude-sandbox-restore-pin.json",
			}
			for _, n := range names {
				Expect(os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o600)).To(Succeed())
			}
			res := tmuxpane.PinRestore(opts())
			Expect(res.Pruned).To(Equal(2))
			Expect(filepath.Join(dir, names[0])).NotTo(BeAnExistingFile())
			Expect(filepath.Join(dir, names[1])).NotTo(BeAnExistingFile())
			Expect(filepath.Join(dir, names[2])).To(BeAnExistingFile())
			Expect(filepath.Join(dir, names[3])).To(BeAnExistingFile())
		})
	})

	Describe("CS-TMUX-065: the pin a --resurrected restore reads", func() {
		write := func(pid int, at time.Time, mut func(*tmuxpane.Pin)) string {
			p := tmuxpane.Pin{V: 1, ServerPID: pid, Server: &tmuxpane.Server{PID: pid, Start: srv.Start}, At: at.UnixMilli(),
				Stamp: stamp(-1), Sidecar: tmuxpane.SidecarName(stamp(-1)), StateFile: tmuxpane.StateFileName(stamp(-1)),
				Preexisting: []string{"%0"}}
			if mut != nil {
				mut(&p)
			}
			b, _ := json.Marshal(p)
			path := filepath.Join(dir, tmuxpane.PinName(pid, at.UnixMilli()))
			Expect(os.WriteFile(path, b, 0o600)).To(Succeed())
			return path
		}

		It("CS-TMUX-065: the newest unconsumed pin of this server, at most 10 minutes old", func() {
			write(4242, now.Add(-5*time.Minute), nil)
			newest := write(4242, now.Add(-time.Minute), func(p *tmuxpane.Pin) { p.Preexisting = []string{"%3"} })
			write(7, now, nil) // another server
			consumed := write(4242, now.Add(-10*time.Second), nil)
			Expect(tmuxpane.ConsumePin(consumed)).To(Succeed())
			p, path, ok := tmuxpane.FindPin(dir, srv, now)
			Expect(ok).To(BeTrue())
			Expect(path).To(Equal(newest))
			Expect(p.Preexisting).To(Equal([]string{"%3"}))
		})

		It("CS-TMUX-065: too old, another start time, or a newest that does not read: no pin, never an older one", func() {
			write(4242, now.Add(-11*time.Minute), nil)
			_, _, ok := tmuxpane.FindPin(dir, srv, now)
			Expect(ok).To(BeFalse())

			write(4242, now.Add(-time.Minute), func(p *tmuxpane.Pin) { p.Server.Start = 1 })
			_, _, ok = tmuxpane.FindPin(dir, srv, now)
			Expect(ok).To(BeFalse(), "a reused pid of another server")

			write(4242, now.Add(-2*time.Minute), nil)
			write(4242, now.Add(-30*time.Second), func(p *tmuxpane.Pin) { p.Sidecar = "../x.json" })
			_, _, ok = tmuxpane.FindPin(dir, srv, now)
			Expect(ok).To(BeFalse())

			bad := write(4242, now.Add(-10*time.Second), nil)
			Expect(os.Chmod(bad, 0o666)).To(Succeed())
			_, _, ok = tmuxpane.FindPin(dir, srv, now)
			Expect(ok).To(BeFalse(), "a world-writable pin fails CS-TMUX-046's checks")
		})
	})

	Describe("CS-TMUX-066, CS-TMUX-067: --rearm", func() {
		var (
			st   string
			rows []tmuxpane.Row
			pin  string
		)
		// panesOut renders list-panes lines: id, coordinates, the server, the
		// current path and the raw mark.
		paneLine := func(id string, w int, path, raw string) string {
			return fmt.Sprintf("%s\tmain\t%d\t0\t4242\t1790000000\t%s\t%s\n", id, w, path, raw)
		}
		BeforeEach(func() {
			st = stamp(-1)
			rows = []tmuxpane.Row{
				{Session: "main", Window: 0, Pane: 0, Mark: mark(0, tmuxpane.StateActive)},  // typed by resurrect
				{Session: "main", Window: 1, Pane: 0, Mark: mark(1, tmuxpane.StatePending)}, // sat at a shell
				{Session: "main", Window: 2, Pane: 0, Mark: mark(2, tmuxpane.StatePending)}, // ran vim
				{Session: "main", Window: 3, Pane: 0, Mark: mark(3, tmuxpane.StateActive)},  // pre-existing
			}
			lines := fmt.Sprintf("pane\tmain\t0\t1\t:*\t0\tt\t:%[1]s\t1\tclaude-sandbox\t:claude-sandbox --new\n"+
				"pane\tmain\t1\t1\t:*\t0\tt\t:%[1]s\t1\tzsh\t:\n"+
				"pane\tmain\t2\t1\t:*\t0\tt\t:%[1]s\t1\tvim\t:vim notes\n"+
				"pane\tmain\t3\t1\t:*\t0\tt\t:%[1]s\t1\tzsh\t:\n", proj)
			save(st, &srv, rows, lines)
			p := tmuxpane.Pin{V: 1, ServerPID: 4242, Server: &srv, At: now.Add(-3 * time.Second).UnixMilli(), Stamp: st,
				Sidecar: tmuxpane.SidecarName(st), StateFile: tmuxpane.StateFileName(st), Preexisting: []string{"%3"}}
			b, _ := json.Marshal(p)
			pin = filepath.Join(dir, tmuxpane.PinName(4242, p.At))
			Expect(os.WriteFile(pin, b, 0o600)).To(Succeed())
			fake.On("tmux list-panes", paneLine("%10", 0, proj, "")+paneLine("%11", 1, proj, "")+
				paneLine("%12", 2, proj, "")+paneLine("%3", 3, proj, ""), nil)
		})
		setOn := func(id string) []tmuxpane.Mark {
			var out []tmuxpane.Mark
			for _, c := range fake.Calls {
				if len(c.Args) == 6 && c.Args[0] == "set-option" && c.Args[3] == id {
					m, ok := tmuxpane.ParseMark(c.Args[5])
					Expect(ok).To(BeTrue())
					out = append(out, m)
				}
			}
			return out
		}
		typed := func() []string {
			var out []string
			for _, l := range fake.CommandLines() {
				if strings.HasPrefix(l, "tmux send-keys") {
					out = append(out, l)
				}
			}
			return out
		}

		It("CS-TMUX-066, CS-TMUX-067: marks every new pane pending, retypes only the pending bare-shell one, consumes the pin", func() {
			res := tmuxpane.Rearm(opts())
			Expect(logged).To(BeEmpty())
			Expect(res.Marked).To(Equal(3))
			Expect(res.Retyped).To(Equal(1))
			for i, id := range []string{"%10", "%11", "%12"} {
				ms := setOn(id)
				Expect(ms).To(HaveLen(1), id)
				Expect(ms[0].State).To(Equal(tmuxpane.StatePending), id)
				Expect(ms[0].Conversation).To(Equal(rows[i].Mark.Conversation), id)
			}
			Expect(setOn("%3")).To(BeEmpty(), "a pre-existing pane is never marked")
			Expect(typed()).To(Equal([]string{"tmux send-keys -t %11 claude-sandbox tmux restore --resurrected C-m"}))
			Expect(res.Consumed).To(BeTrue())
			Expect(pin).NotTo(BeAnExistingFile())
			Expect(strings.TrimSuffix(pin, ".json") + ".consumed.json").To(BeAnExistingFile())
			Expect(fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux display-message")))
		})

		It("CS-TMUX-066: a pane already marked (by a list or a re-check) is left to its own restore", func() {
			fake = &execx.Fake{}
			fake.On("tmux list-panes", paneLine("%10", 0, proj, mark(0, tmuxpane.StatePending).JSON())+paneLine("%11", 1, proj, "")+
				paneLine("%12", 2, proj, ""), nil)
			fake.On("show-options -p -q -v -t %11", mark(1, tmuxpane.StatePending).JSON()+"\n", nil)
			res := tmuxpane.Rearm(opts())
			Expect(setOn("%10")).To(BeEmpty())
			Expect(setOn("%11")).To(BeEmpty())
			Expect(setOn("%12")).To(HaveLen(1))
			Expect(typed()).To(BeEmpty())
			Expect(res.Skipped).To(Equal(2))
		})

		It("CS-TMUX-066: a pane not in its saved dir, or an active row saved outside its project, is not armed", func() {
			elsewhere := GinkgoT().TempDir()
			fake = &execx.Fake{}
			fake.On("tmux list-panes", paneLine("%10", 0, proj, "")+paneLine("%11", 1, elsewhere, "")+paneLine("%12", 2, proj, ""), nil)
			rows[0].Mark.Project = elsewhere // saved dir is proj: the active row's layout moved
			save(st, &srv, rows, "")
			res := tmuxpane.Rearm(opts())
			Expect(setOn("%10")).To(BeEmpty())
			Expect(setOn("%11")).To(BeEmpty())
			Expect(setOn("%12")).To(HaveLen(1))
			Expect(res.Skipped).To(Equal(2))
			Expect(logged).To(ContainElement(ContainSubstring("main:0.0: its saved directory is not the session's project")))
			Expect(logged).To(ContainElement(ContainSubstring("main:1.0: the pane is not in its saved directory")))
		})

		It("CS-TMUX-066: a saved dir holding whitespace is not compared; the first escaped space is undone", func() {
			spaced := filepath.Join(proj, "my dir")
			Expect(os.Mkdir(spaced, 0o700)).To(Succeed())
			// "my\ dir" (resurrect's escape) matches; a pane elsewhere still
			// arms, since a whitespace dir is not compared.
			lines := fmt.Sprintf("pane\tmain\t1\t1\t:*\t0\tt\t:%s\t1\tzsh\t:\n", strings.Replace(spaced, " ", `\ `, 1))
			save(st, &srv, rows[1:2], lines)
			fake = &execx.Fake{}
			fake.On("tmux list-panes", paneLine("%11", 1, GinkgoT().TempDir(), ""), nil)
			res := tmuxpane.Rearm(opts())
			Expect(res.Marked).To(Equal(1))
			Expect(res.Retyped).To(Equal(1))
		})

		It("CS-TMUX-067: an active row at a bare shell is marked, never typed into", func() {
			lines := fmt.Sprintf("pane\tmain\t0\t1\t:*\t0\tt\t:%s\t1\tzsh\t:\n", proj)
			save(st, &srv, rows[0:1], lines)
			res := tmuxpane.Rearm(opts())
			Expect(res.Marked).To(Equal(1))
			Expect(typed()).To(BeEmpty())
		})

		It("CS-TMUX-067: a line without field 11 is marked, never typed into", func() {
			lines := fmt.Sprintf("pane\tmain\t1\t1\t:*\t0\tt\t:%s\t1\tzsh\n", proj)
			save(st, &srv, rows[1:2], lines)
			res := tmuxpane.Rearm(opts())
			Expect(res.Marked).To(Equal(1))
			Expect(typed()).To(BeEmpty())
		})

		It("CS-TMUX-066: no pin of this server: nothing marked, one log line", func() {
			Expect(os.Remove(pin)).To(Succeed())
			res := tmuxpane.Rearm(opts())
			Expect(res.Marked).To(BeZero())
			Expect(fake.CommandLines()).To(HaveLen(1), "only the list")
			Expect(logged).To(ConsistOf(ContainSubstring("no usable pin of this tmux server")))
		})

		It("CS-TMUX-066: rows left at the deadline are logged and the pin stays unconsumed", func() {
			tmuxpane.RearmDeadline = 30 * time.Millisecond
			fake.OnFunc("tmux show-options", func(execx.Cmd) (string, error) { time.Sleep(40 * time.Millisecond); return "", nil })
			res := tmuxpane.Rearm(opts())
			Expect(res.Late).To(BeNumerically(">", 0))
			Expect(res.Consumed).To(BeFalse())
			Expect(pin).To(BeAnExistingFile())
			Expect(logged).To(ContainElement(ContainSubstring("left to their typed restore")))
		})

		It("CS-TMUX-066: a pinned save without a sidecar arms nothing and consumes the pin", func() {
			Expect(os.Remove(filepath.Join(dir, tmuxpane.SidecarName(st)))).To(Succeed())
			res := tmuxpane.Rearm(opts())
			Expect(res.Marked).To(BeZero())
			Expect(res.Consumed).To(BeTrue())
		})
	})

	It("CS-TMUX-067: the state file parser says whether field 11 was saved at all", func() {
		ps := tmuxpane.ParseStateFile([]byte("pane\tmain\t1\t1\t:*\t0\tt\t:/p\t1\tzsh\t:\npane\tmain\t2\t1\t:*\t0\tt\t:/p\t1\tzsh\n"))
		Expect(ps).To(HaveLen(2))
		Expect(ps[0].FullCommandSaved).To(BeTrue())
		Expect(ps[0].FullCommand).To(BeEmpty())
		Expect(ps[1].FullCommandSaved).To(BeFalse())
	})

	It("CS-TMUX-065: PrintNotice prints a notice without claiming it; one older than 7 days is removed", func() {
		Expect(tmuxpane.WriteNotice(cache, tmuxpane.Notice{Stamp: stamp(-1), N: 1, M: 9, K: 3, Lifetimes: true, At: now.UnixMilli()})).To(Succeed())
		out := &bytes.Buffer{}
		line := tmuxpane.PrintNotice(cache, now, out)
		Expect(out.String()).To(Equal(line + "\n"))
		Expect(filepath.Join(cache, tmuxpane.NoticeFile)).To(BeAnExistingFile())
		out.Reset()
		Expect(tmuxpane.PrintNotice(cache, now.Add(8*24*time.Hour), out)).To(BeEmpty())
		Expect(out.String()).To(BeEmpty())
		Expect(filepath.Join(cache, tmuxpane.NoticeFile)).NotTo(BeAnExistingFile())
	})
})
