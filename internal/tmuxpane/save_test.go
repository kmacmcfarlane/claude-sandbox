package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-030..040) — the tmux-resurrect
// post-save-layout hook: tmux and docker faked through execx.Fake, the
// resurrect dir and the registry in scratch directories. The command wiring
// (silence, exit 0, the log) is in cmd/claude-sandbox/tmux_save_cli_test.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

const (
	saveID  = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	saveID2 = "9f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	conv2   = "1b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	conv3   = "2b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
)

// stallRunner is a Fake whose Start hangs for commands matching hang, until
// signalled — a tmux or docker that never answers.
type stallRunner struct {
	*execx.Fake
	hang []string
}

type stalledProcess struct {
	once sync.Once
	done chan struct{}
}

func (p *stalledProcess) Signal(os.Signal) error { p.once.Do(func() { close(p.done) }); return nil }
func (p *stalledProcess) Wait() error            { <-p.done; return fmt.Errorf("killed") }
func (p *stalledProcess) Pid() int               { return 4343 }

func (h *stallRunner) Start(c execx.Cmd) (execx.Process, error) {
	line := c.Name + " " + strings.Join(c.Args, " ")
	for _, p := range h.hang {
		if strings.Contains(line, p) {
			h.Fake.Calls = append(h.Fake.Calls, c)
			return &stalledProcess{done: make(chan struct{})}, nil
		}
	}
	return h.Fake.Start(c)
}

var _ = Describe("tmux save hook (CS-TMUX-030..040)", func() {
	var (
		dir, reg, proj, home, state string
		fake                        *execx.Fake
		logs                        []string
		now                         = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		since                       = now.Add(-time.Hour).UnixMilli()
	)

	mark := func(mut func(*tmuxpane.Mark)) tmuxpane.Mark {
		m := tmuxpane.Mark{
			V: 1, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: "claude-sandbox-work-proj-abc123-murre", ContainerID: saveID,
			Instance: "murre", Project: proj, CwdRoot: proj, Class: "7", Since: since,
			ConfigDir: filepath.Join(home, ".claude"), RegistryDir: reg,
		}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	paneRow := func(session string, w, p int, id, cmd string, m *tmuxpane.Mark) string {
		raw := ""
		if m != nil {
			raw = m.JSON()
		}
		return fmt.Sprintf("%s\t%d\t%d\t%s\t%s\t%s", session, w, p, id, cmd, raw)
	}
	stateLine := func(session string, w, p int, dir, full string) string {
		return strings.Join([]string{"pane", session, fmt.Sprint(w), "1", ":*", fmt.Sprint(p), "title", ":" + dir, "1", "claude-sandbox", ":" + full}, "\t")
	}
	writeState := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		Expect(os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)).To(Succeed())
		return p
	}
	record := func(pid int, sessionID, cwd string, startedAt int64, name, source string) {
		b, _ := json.Marshal(map[string]any{
			"pid": pid, "sessionId": sessionID, "cwd": cwd, "startedAt": startedAt,
			"name": name, "nameSource": source, "procStart": "123", "pidDomain": "linux::pid:[1]",
		})
		Expect(os.WriteFile(filepath.Join(reg, fmt.Sprintf("%d.json", pid)), b, 0o600)).To(Succeed())
	}
	listPanes := func(rows ...string) { fake.On("tmux list-panes", strings.Join(rows, "\n")+"\n", nil) }
	dockerPS := func(rows ...string) { fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil) }
	opts := func() tmuxpane.SaveOptions {
		return tmuxpane.SaveOptions{
			Runner: fake, Now: func() time.Time { return now }, Home: home,
			Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		}
	}
	readSidecar := func(p string) tmuxpane.Sidecar {
		b, err := os.ReadFile(p)
		Expect(err).NotTo(HaveOccurred())
		var sc tmuxpane.Sidecar
		Expect(json.Unmarshal(b, &sc)).To(Succeed())
		return sc
	}
	writeBacks := func() []tmuxpane.Mark {
		var out []tmuxpane.Mark
		for _, c := range fake.Calls {
			if c.Name == "tmux" && len(c.Args) == 6 && c.Args[0] == "set-option" {
				m, ok := tmuxpane.ParseMark(c.Args[5])
				Expect(ok).To(BeTrue())
				out = append(out, m)
			}
		}
		return out
	}
	commands := func(prefix string) int {
		n := 0
		for _, l := range fake.CommandLines() {
			if strings.HasPrefix(l, prefix) {
				n++
			}
		}
		return n
	}

	BeforeEach(func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		dir = filepath.Join(base, "resurrect")
		home = filepath.Join(base, "home")
		reg = filepath.Join(home, ".claude", "sessions")
		proj = filepath.Join(base, "work", "proj")
		for _, d := range []string{dir, reg, proj} {
			Expect(os.MkdirAll(d, 0o700)).To(Succeed())
		}
		fake = &execx.Fake{}
		logs = nil
		state = writeState("tmux_resurrect_20260929T120000.txt", stateLine("main", 1, 0, proj, "claude-sandbox"))
		DeferCleanup(func(d, c time.Duration) { tmuxpane.SaveDeadline, tmuxpane.CallTimeout = d, c },
			tmuxpane.SaveDeadline, tmuxpane.CallTimeout)
	})

	Describe("CS-TMUX-030: guards", func() {
		It("CS-TMUX-030: refuses anything but an absolute, regular tmux_resurrect_*.txt and runs nothing", func() {
			other := filepath.Join(dir, "notes.txt")
			Expect(os.WriteFile(other, []byte("pane\n"), 0o600)).To(Succeed())
			link := filepath.Join(dir, "tmux_resurrect_20260929T130000.txt")
			Expect(os.Symlink(state, link)).To(Succeed())
			sub := filepath.Join(dir, "tmux_resurrect_x.txt")
			Expect(os.Mkdir(sub, 0o700)).To(Succeed())
			for _, arg := range []string{"", "tmux_resurrect_20260929T120000.txt", other, link, sub,
				filepath.Join(dir, "tmux_resurrect_missing.txt")} {
				_, err := tmuxpane.Save(arg, opts())
				Expect(err).To(MatchError(tmuxpane.ErrNotStateFile), arg)
			}
			Expect(fake.Calls).To(BeEmpty())
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				Expect(e.Name()).NotTo(HaveSuffix(".claude-sandbox.json"))
			}
		})
	})

	Describe("CS-TMUX-031: panes", func() {
		It("CS-TMUX-031: one list-panes; only panes on a pane line of the state file, with a v1 active or pending mark", func() {
			state = writeState("tmux_resurrect_20260929T120100.txt",
				stateLine("main", 1, 0, proj, "claude-sandbox"),
				stateLine("main", 2, 0, proj, "claude-sandbox"),
				stateLine("main", 3, 0, proj, ""),
				"window\tmain\t1\t:proj\t1\t:*\tlayout\t:")
			m := mark(nil)
			bad := mark(func(m *tmuxpane.Mark) { m.State = "weird" })
			v2 := mark(func(m *tmuxpane.Mark) { m.V = 2 })
			listPanes(
				paneRow("main", 1, 0, "%1", "claude-sandbox", &m),
				paneRow("grouped", 1, 0, "%2", "claude-sandbox", &m), // not saved: a grouped session
				paneRow("main", 2, 0, "%3", "claude-sandbox", &bad),
				paneRow("main", 3, 0, "%4", "claude-sandbox", nil),
				paneRow("main", 9, 0, "%5", "claude-sandbox", &v2))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(commands("tmux list-panes")).To(Equal(1))
			Expect(fake.CommandLines()[0]).To(Equal("tmux list-panes -a -F #{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{@claude-sandbox}"))
			Expect(res.Rows).To(HaveLen(1))
			Expect(res.Rows[0].Session).To(Equal("main"))
			Expect(res.Rows[0].Window).To(Equal(1))
			Expect(res.Rows[0].Pane).To(Equal(0))
		})

		It("CS-TMUX-031: a failed list-panes writes no sidecar and leaves the previous one alone", func() {
			prev := tmuxpane.SidecarPath(state)
			Expect(os.WriteFile(prev, []byte(`{"v":1,"old":true}`), 0o600)).To(Succeed())
			fake.On("tmux list-panes", "", execx.Fail(1))
			_, err := tmuxpane.Save(state, opts())
			Expect(err).To(HaveOccurred())
			Expect(os.ReadFile(prev)).To(Equal([]byte(`{"v":1,"old":true}`)))
			Expect(commands("docker")).To(Equal(0))
		})
	})

	Describe("CS-TMUX-032: liveness", func() {
		It("CS-TMUX-032: an active mark under a shell prompt is dropped without asking docker", func() {
			m := mark(nil)
			listPanes(paneRow("main", 1, 0, "%1", "zsh", &m))
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows).To(BeEmpty())
			Expect(commands("docker")).To(Equal(0))
			Expect(readSidecar(res.Sidecar).Panes).To(BeEmpty())
		})

		It("CS-TMUX-032: one bounded docker ps; an active mark whose container is gone or exited is dropped", func() {
			state = writeState("tmux_resurrect_20260929T120200.txt",
				stateLine("main", 1, 0, proj, ""), stateLine("main", 2, 0, proj, ""),
				stateLine("main", 3, 0, proj, ""), stateLine("main", 4, 0, proj, ""))
			live := mark(nil)
			gone := mark(func(m *tmuxpane.Mark) { m.ContainerID = saveID2; m.Container = "c-gone"; m.Class = "8" })
			exited := mark(func(m *tmuxpane.Mark) { m.ContainerID = ""; m.Container = "c-exited"; m.Class = "9" })
			byName := mark(func(m *tmuxpane.Mark) { m.ContainerID = ""; m.Container = "c-created"; m.Class = "10" })
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &live), paneRow("main", 2, 0, "%2", "claude-sandbox", &gone),
				paneRow("main", 3, 0, "%3", "claude-sandbox", &exited), paneRow("main", 4, 0, "%4", "claude-sandbox", &byName))
			dockerPS(saveID+"\t"+live.Container+"\trunning", "aaaa\t/c-exited\texited", "bbbb\tc-created\tcreated")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(commands("docker ps -a --no-trunc --filter label=claude-sandbox.project --format {{.ID}}\t{{.Names}}\t{{.State}}")).To(Equal(1))
			var names []string
			for _, r := range res.Rows {
				names = append(names, r.Mark.Container)
			}
			Expect(names).To(Equal([]string{live.Container, "c-created"}))
			Expect(fake.Calls[1].DieWithParent).To(BeTrue(), "docker runs in its own process group, killed with the hook")
		})

		It("CS-TMUX-032: docker trouble keeps every active mark", func() {
			m := mark(nil)
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			fake.On("docker ps", "", execx.Fail(1))
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows).To(HaveLen(1))
			Expect(strings.Join(logs, "\n")).To(ContainSubstring("docker ps failed"))
		})

		It("CS-TMUX-032: a pending mark is recorded verbatim whatever the pane runs, with no docker and no registry", func() {
			record(7, conv2, proj, since+1000, "renamed", "user")
			p := mark(func(m *tmuxpane.Mark) { m.State = tmuxpane.StatePending; m.Conversation = convID; m.Name = "old" })
			listPanes(paneRow("main", 1, 0, "%1", "zsh", &p))
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows).To(HaveLen(1))
			Expect(res.Rows[0].Mark).To(Equal(p))
			Expect(commands("docker")).To(Equal(0))
			Expect(writeBacks()).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-033: registry match", func() {
		run := func(m tmuxpane.Mark) tmuxpane.Mark {
			fake = &execx.Fake{} // each run lists only this mark
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows).To(HaveLen(1))
			return res.Rows[0].Mark
		}

		It("CS-TMUX-033: class, cwd under project or cwdRoot, startedAt >= since - 5 s, earliest wins", func() {
			record(7, convID, proj, since-4000, "primary", "user")             // the primary, within the slack
			record(263, conv2, filepath.Join(proj, "sub"), since+9000, "", "") // a join at class + 256
			record(8, conv3, proj, since-1000, "", "")                         // another class
			record(519, conv3, "/elsewhere", since-4500, "", "")               // right class, wrong cwd
			got := run(mark(nil))
			Expect(got.Conversation).To(Equal(convID))
			Expect(got.Name).To(Equal("primary"))
			Expect(got.NameSource).To(Equal("user"))
		})

		It("CS-TMUX-033: with the primary's record absent, a later join's record in the same container is a miss", func() {
			record(263, conv2, proj, since+10*60*1000, "join", "user") // a join ten minutes in
			m := mark(func(m *tmuxpane.Mark) { m.Conversation, m.Name, m.NameSource = convID, "primary", "user" })
			got := run(m)
			Expect(got.Conversation).To(Equal(convID), "the mark keeps its own id")
			Expect(got.Name).To(Equal("primary"))
			Expect(run(mark(nil)).Conversation).To(BeEmpty(), "and a mark with no id yet stays empty")
		})

		It("CS-TMUX-033: a primary's record must start within PrimaryWindow of since, a join's within JoinWindow", func() {
			record(7, convID, proj, since+tmuxpane.PrimaryWindow.Milliseconds()+1, "", "")
			Expect(run(mark(nil)).Conversation).To(BeEmpty())
			record(7, convID, proj, since+tmuxpane.PrimaryWindow.Milliseconds(), "", "")
			Expect(run(mark(nil)).Conversation).To(Equal(convID))
			record(7, conv2, proj, since+tmuxpane.JoinWindow.Milliseconds()+1, "", "")
			Expect(run(mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin })).Conversation).To(BeEmpty())
		})

		It("CS-TMUX-033: an attach mark with since 0 (creation time unknown) matches without a window", func() {
			record(7, convID, proj, since, "", "")
			Expect(run(mark(func(m *tmuxpane.Mark) { m.Since = 0 })).Conversation).To(Equal(convID))
		})

		It("CS-TMUX-033: a candidate naming the mark's current conversation wins over an earlier one", func() {
			record(7, conv2, proj, since, "", "")
			record(263, convID, proj, since+3000, "", "")
			m := mark(func(m *tmuxpane.Mark) { m.Conversation = convID })
			Expect(run(m).Conversation).To(Equal(convID))
			Expect(run(mark(nil)).Conversation).To(Equal(conv2), "without a current id the earliest wins")
		})

		It("CS-TMUX-033: a record older than since - 5 s never counts", func() {
			record(7, convID, proj, since-6000, "stale", "user")
			Expect(run(mark(nil)).Conversation).To(BeEmpty())
		})

		It("CS-TMUX-033: a record under cwdRoot (a worktree) counts", func() {
			wt := filepath.Join(proj, ".claude", "worktrees", "murre")
			record(7, convID, wt, since, "", "")
			Expect(run(mark(func(m *tmuxpane.Mark) { m.Worktree = "murre"; m.CwdRoot = wt })).Conversation).To(Equal(convID))
		})

		It("CS-TMUX-033: a join takes the earliest record with startedAt >= since", func() {
			record(7, convID, proj, since-1000, "", "")   // the primary, before the join's exec
			record(263, conv2, proj, since+2000, "", "")  // this join
			record(519, conv3, proj, since+60000, "", "") // a later join
			Expect(run(mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin })).Conversation).To(Equal(conv2))
		})

		It("CS-TMUX-033: a ralph mark is not resolved", func() {
			record(7, convID, proj, since, "", "")
			Expect(run(mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph; m.Instance = "" })).Conversation).To(BeEmpty())
		})

		It("CS-TMUX-033: without registryDir it tries <CLAUDE_CONFIG_DIR or ~/.claude>/sessions, then the peers dir", func() {
			peers := filepath.Join(home, ".cache", "claude-sandbox", "peers", "sessions")
			Expect(os.MkdirAll(peers, 0o700)).To(Succeed())
			b, _ := json.Marshal(map[string]any{"pid": 7, "sessionId": conv2, "cwd": proj, "startedAt": since})
			Expect(os.WriteFile(filepath.Join(peers, "7.json"), b, 0o600)).To(Succeed())
			Expect(run(mark(func(m *tmuxpane.Mark) { m.RegistryDir = "" })).Conversation).To(Equal(conv2))

			fake = &execx.Fake{}
			record(7, convID, proj, since, "", "") // ~/.claude/sessions now holds one
			Expect(run(mark(func(m *tmuxpane.Mark) { m.RegistryDir = "" })).Conversation).To(Equal(convID))
		})
	})

	Describe("CS-TMUX-034: defensive reads", func() {
		It("CS-TMUX-034: only a valid regular record of at most 64 KiB, pid matching its name, with a UUID", func() {
			w := func(name, body string) {
				Expect(os.WriteFile(filepath.Join(reg, name), []byte(body), 0o600)).To(Succeed())
			}
			w("7.json", fmt.Sprintf(`{"pid":263,"sessionId":%q,"cwd":%q,"startedAt":%d}`, convID, proj, since)) // pid mismatch
			w("263.json", fmt.Sprintf(`{"pid":263,"sessionId":"not-a-uuid","cwd":%q,"startedAt":%d}`, proj, since))
			w("519.json", "{not json")
			w("775.json", fmt.Sprintf(`{"pid":775,"sessionId":%q,"cwd":%q,"startedAt":%d,"pad":%q}`, convID, proj, since, strings.Repeat("x", 70<<10)))
			Expect(os.Symlink(filepath.Join(reg, "263.json"), filepath.Join(reg, "1031.json"))).To(Succeed())
			w("x7.json", "{}")
			recs, err := tmuxpane.ReadRegistry(reg)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(BeEmpty())

			w("1287.json", fmt.Sprintf(`{"pid":1287,"sessionId":%q,"cwd":%q,"startedAt":%d,"name":"a\u001b[31mb\tc","nameSource":"user","secret":"x"}`, convID, proj, since))
			recs, err = tmuxpane.ReadRegistry(reg)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(Equal([]tmuxpane.RegistryRecord{{PID: 1287, SessionID: convID, Cwd: proj, StartedAt: since, Name: "a [31mb c", NameSource: "user"}}))
		})

		It("CS-TMUX-034: a symlinked registry dir is not read", func() {
			link := filepath.Join(home, "reglink")
			Expect(os.Symlink(reg, link)).To(Succeed())
			_, err := tmuxpane.ReadRegistry(link)
			Expect(err).To(HaveOccurred())
		})

		It("CS-TMUX-034: a FIFO record never blocks", func() {
			Expect(mkfifo(filepath.Join(reg, "7.json"))).To(Succeed())
			done := make(chan struct{})
			go func() { tmuxpane.ReadRegistry(reg); close(done) }()
			Eventually(done, 2*time.Second).Should(BeClosed())
		})

		It("CS-TMUX-034: names: control characters stripped, over 200 characters or a leading '-' absent; name sources from the 2.1.284 set", func() {
			Expect(tmuxpane.CleanName("  fix\x07 the\nbug ")).To(Equal("fix the bug"))
			Expect(tmuxpane.CleanName("--dangerous")).To(BeEmpty())
			// Format (Cf: bidi overrides, zero-width), private-use (Co) and
			// surrogate (Cs, as invalid UTF-8 decodes to U+FFFD, kept) characters.
			Expect(tmuxpane.CleanName("safe\u202egnp.exe\u200b\ue000")).To(Equal("safegnp.exe"))
			Expect(tmuxpane.CleanName("\u2066-x\u2069")).To(BeEmpty(), "a hidden character cannot hide a leading '-'")
			Expect(tmuxpane.CleanName(strings.Repeat("é", 201))).To(BeEmpty())
			Expect(tmuxpane.CleanName(strings.Repeat("é", 200))).To(HaveLen(400))
			for _, s := range []string{"user", "peer", "derived", "collision", "auto", "hook"} {
				Expect(tmuxpane.NameSources[s]).To(BeTrue(), s)
			}
			record(7, convID, proj, since, "n", "made-up")
			recs, _ := tmuxpane.ReadRegistry(reg)
			Expect(recs[0].NameSource).To(BeEmpty())
			record(7, convID, proj, since, "", "user")
			recs, _ = tmuxpane.ReadRegistry(reg)
			Expect(recs[0].NameSource).To(BeEmpty(), "no name, no name source")
		})
	})

	Describe("CS-TMUX-035: write-back and carry-forward", func() {
		It("CS-TMUX-035: a hit is written back with one set-option; an unchanged mark gets none", func() {
			record(7, convID, proj, since, "my task", "user")
			m := mark(nil)
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			fake.On("tmux show-options", m.JSON()+"\n", nil)
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.WriteBacks).To(Equal(1))
			wb := writeBacks()
			Expect(wb).To(HaveLen(1))
			Expect(wb[0].Conversation).To(Equal(convID))
			Expect(wb[0].Name).To(Equal("my task"))
			last := fake.Calls[len(fake.Calls)-1]
			Expect(last.Args[:5]).To(Equal([]string{"set-option", "-p", "-t", "%1", tmuxpane.Option}))
			Expect(last.DieWithParent).To(BeTrue())

			updated := wb[0]
			fake = &execx.Fake{}
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &updated))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err = tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.WriteBacks).To(Equal(0))
			Expect(writeBacks()).To(BeEmpty())
		})

		It("CS-TMUX-070: a mark that changed between the list and the write is not overwritten", func() {
			record(7, convID, proj, since, "my task", "user")
			m := mark(nil)
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			relaunched := mark(func(m *tmuxpane.Mark) { m.Since = since + 60000; m.Container = "c-new" })
			fake.On("tmux show-options", relaunched.JSON()+"\n", nil)
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.WriteBacks).To(Equal(0))
			Expect(res.Superseded).To(Equal(1))
			Expect(writeBacks()).To(BeEmpty())
			Expect(fake.CommandLines()).To(ContainElement("tmux show-options -p -q -v -t %1 @claude-sandbox"))

			fake = &execx.Fake{}
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			fake.On("tmux show-options", "", nil) // unmarked since: the session ended
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err = tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Superseded).To(Equal(1))
			Expect(writeBacks()).To(BeEmpty())
		})

		It("CS-TMUX-035: a miss keeps the mark's last good id", func() {
			m := mark(func(m *tmuxpane.Mark) { m.Conversation, m.Name, m.NameSource = convID, "kept", "user" })
			Expect(os.RemoveAll(reg)).To(Succeed()) // the registry is unreadable
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows[0].Mark).To(Equal(m))
			Expect(writeBacks()).To(BeEmpty())
		})

		It("CS-TMUX-035: a /clear (a new id at the same record) replaces the id", func() {
			record(7, conv2, proj, since, "", "derived")
			m := mark(func(m *tmuxpane.Mark) { m.Conversation, m.Name, m.NameSource = convID, "old", "user" })
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Rows[0].Mark.Conversation).To(Equal(conv2))
			Expect(res.Rows[0].Mark.Name).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-036: generated worktree names", func() {
		join := func(cwd string) tmuxpane.Mark {
			record(263, convID, cwd, since+1000, "", "")
			m := mark(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin; m.WorktreeGenerated = true })
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			return res.Rows[0].Mark
		}

		It("CS-TMUX-036: the record's cwd under <cwdRoot>/.claude/worktrees/<name> fills worktree and cwdRoot", func() {
			got := join(filepath.Join(proj, ".claude", "worktrees", "swift-otter-3f2a", "pkg"))
			Expect(got.Worktree).To(Equal("swift-otter-3f2a"))
			Expect(got.CwdRoot).To(Equal(filepath.Join(proj, ".claude", "worktrees", "swift-otter-3f2a")))
			Expect(got.Conversation).To(Equal(convID))
			Expect(tmuxpane.ResumeCommand(got)).To(ContainSubstring("--worktree=swift-otter-3f2a"))
		})

		It("CS-TMUX-036: any other cwd leaves the worktree unknown, never the shared checkout", func() {
			got := join(proj)
			Expect(got.Worktree).To(BeEmpty())
			Expect(got.WorktreeGenerated).To(BeTrue())
			Expect(tmuxpane.ResumeCommand(got)).To(BeEmpty())
		})
	})

	Describe("CS-TMUX-037: sidecar target", func() {
		It("CS-TMUX-037: equal to last's target: the sidecar is written for last's target", func() {
			prev := writeState("tmux_resurrect_20260929T115900.txt", stateLine("main", 1, 0, proj, "claude-sandbox"))
			Expect(os.Symlink(filepath.Base(prev), filepath.Join(dir, "last"))).To(Succeed())
			listPanes()
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sidecar).To(Equal(filepath.Join(dir, "tmux_resurrect_20260929T115900.claude-sandbox.json")))
			Expect(readSidecar(res.Sidecar).StateFile).To(Equal("tmux_resurrect_20260929T115900.txt"))
			Expect(tmuxpane.SidecarPath(state)).NotTo(BeAnExistingFile())
		})

		It("CS-TMUX-037: different from last's target: the sidecar is written for the new file", func() {
			prev := writeState("tmux_resurrect_20260929T115900.txt", stateLine("main", 2, 0, proj, ""))
			Expect(os.Symlink(filepath.Base(prev), filepath.Join(dir, "last"))).To(Succeed())
			listPanes()
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sidecar).To(Equal(filepath.Join(dir, "tmux_resurrect_20260929T120000.claude-sandbox.json")))
			Expect(tmuxpane.IsStateFileName(filepath.Base(res.Sidecar))).To(BeFalse(), "outside resurrect's prune glob")
		})
	})

	Describe("CS-TMUX-038: the sidecar", func() {
		It("CS-TMUX-038: compact JSON, 0600, rows of coordinates plus the updated mark", func() {
			record(7, convID, proj, since, "t", "user")
			m := mark(nil)
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m))
			dockerPS(saveID + "\t" + m.Container + "\trunning")
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			fi, err := os.Stat(res.Sidecar)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
			raw, _ := os.ReadFile(res.Sidecar)
			Expect(strings.Count(string(raw), "\n")).To(Equal(1), "compact")
			var generic map[string]any
			Expect(json.Unmarshal(raw, &generic)).To(Succeed())
			Expect(generic).To(HaveKeyWithValue("v", BeNumerically("==", 1)))
			Expect(generic).To(HaveKeyWithValue("stateFile", "tmux_resurrect_20260929T120000.txt"))
			Expect(generic).To(HaveKeyWithValue("savedAt", BeNumerically("==", now.UnixMilli())))
			row := generic["panes"].([]any)[0].(map[string]any)
			Expect(row).To(HaveKeyWithValue("session", "main"))
			Expect(row).To(HaveKeyWithValue("window", BeNumerically("==", 1)))
			Expect(row).To(HaveKeyWithValue("pane", BeNumerically("==", 0)))
			Expect(row["mark"]).To(HaveKeyWithValue("conversation", convID))
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				Expect(e.Name()).NotTo(HavePrefix(".claude-sandbox-save-"), "no temp file left")
			}
		})

		It("CS-TMUX-038: no sandbox panes: a sidecar with no rows", func() {
			listPanes(paneRow("main", 1, 0, "%1", "zsh", nil))
			res, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			raw, _ := os.ReadFile(res.Sidecar)
			Expect(string(raw)).To(ContainSubstring(`"panes":[]`))
		})

		It("CS-TMUX-038: a failed write leaves the previous sidecar alone", func() {
			prev := tmuxpane.SidecarPath(state)
			Expect(os.WriteFile(prev, []byte("previous"), 0o600)).To(Succeed())
			Expect(os.Chmod(dir, 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, dir, os.FileMode(0o700))
			listPanes()
			_, err := tmuxpane.Save(state, opts())
			Expect(err).To(HaveOccurred())
			Expect(os.ReadFile(prev)).To(Equal([]byte("previous")))
		})
	})

	Describe("CS-TMUX-039: prune", func() {
		It("CS-TMUX-039: removes orphaned sidecars and old temp files, nothing else", func() {
			keep := writeState("tmux_resurrect_20260101T000000.txt", "")
			touch := func(name string, age time.Duration) string {
				p := filepath.Join(dir, name)
				Expect(os.WriteFile(p, []byte("x"), 0o600)).To(Succeed())
				Expect(os.Chtimes(p, now.Add(-age), now.Add(-age))).To(Succeed())
				return p
			}
			kept := touch("tmux_resurrect_20260101T000000.claude-sandbox.json", 0)
			orphan := touch("tmux_resurrect_20250101T000000.claude-sandbox.json", 0)
			oldTmp := touch(".claude-sandbox-save-123.tmp", 2*time.Hour)
			newTmp := touch(".claude-sandbox-save-456.tmp", time.Minute)
			others := []string{touch("claude-sandbox-restore-pin.1.2.json", 48*time.Hour), touch("notes.json", 48*time.Hour), touch("tmux_resurrect_x.txt.bak", 48*time.Hour)}
			listPanes()
			_, err := tmuxpane.Save(state, opts())
			Expect(err).NotTo(HaveOccurred())
			Expect(orphan).NotTo(BeAnExistingFile())
			Expect(oldTmp).NotTo(BeAnExistingFile())
			for _, p := range append([]string{keep, kept, newTmp}, others...) {
				Expect(p).To(BeAnExistingFile())
			}
		})
	})

	Describe("CS-TMUX-040: bounded", func() {
		It("CS-TMUX-040: a hung list-panes is killed at the call timeout", func() {
			tmuxpane.CallTimeout = 50 * time.Millisecond
			r := &stallRunner{Fake: fake, hang: []string{"tmux list-panes"}}
			o := opts()
			o.Runner = r
			t0 := time.Now()
			_, err := tmuxpane.Save(state, o)
			Expect(err).To(HaveOccurred())
			Expect(time.Since(t0)).To(BeNumerically("<", time.Second))
		})

		It("CS-TMUX-040: a hung docker keeps the marks; write-backs past the deadline are skipped, the sidecar written first", func() {
			tmuxpane.CallTimeout = 100 * time.Millisecond
			tmuxpane.SaveDeadline = 250 * time.Millisecond
			state = writeState("tmux_resurrect_20260929T120300.txt",
				stateLine("main", 1, 0, proj, ""), stateLine("main", 2, 0, proj, ""), stateLine("main", 3, 0, proj, ""))
			record(7, convID, proj, since, "", "")
			record(8, conv2, proj, since, "", "")
			record(9, conv3, proj, since, "", "")
			m7 := mark(nil)
			m8 := mark(func(m *tmuxpane.Mark) { m.Class = "8" })
			m9 := mark(func(m *tmuxpane.Mark) { m.Class = "9" })
			listPanes(paneRow("main", 1, 0, "%1", "claude-sandbox", &m7), paneRow("main", 2, 0, "%2", "claude-sandbox", &m8),
				paneRow("main", 3, 0, "%3", "claude-sandbox", &m9))
			r := &stallRunner{Fake: fake, hang: []string{"docker ps", "tmux show-options", "tmux set-option"}}
			o := opts()
			o.Runner = r
			t0 := time.Now()
			res, err := tmuxpane.Save(state, o)
			Expect(err).NotTo(HaveOccurred())
			Expect(time.Since(t0)).To(BeNumerically("<", 600*time.Millisecond))
			Expect(res.Rows).To(HaveLen(3), "docker trouble keeps the marks")
			Expect(readSidecar(res.Sidecar).Panes).To(HaveLen(3))
			Expect(res.WriteBacks).To(Equal(0))
			Expect(res.Skipped).To(BeNumerically(">=", 1))
			Expect(strings.Join(logs, "\n")).To(ContainSubstring("left for the next save"))
		})
	})
})

// mkfifo makes a named pipe, a record that would block a plain open.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
