package tmuxpane_test

// Spec: spec/tmux.feature (CS-TMUX-051) — the restore decision table as a
// pure function over scripted probes, and the read-only probes themselves
// (tmux and docker faked through execx.Fake).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

// probes is a scripted tmuxpane.Probes that records what was asked.
type probes struct {
	dirs      map[string]bool
	dockerErr error
	info      tmuxpane.ContainerInfo
	inspErr   error
	onScreen  string
	guard     tmuxpane.GuardResult
	gap       time.Duration
	asked     []string
}

func (p *probes) IsDir(path string) bool { p.asked = append(p.asked, "dir"); return p.dirs[path] }
func (p *probes) Docker() error          { p.asked = append(p.asked, "docker"); return p.dockerErr }
func (p *probes) Inspect(id string) (tmuxpane.ContainerInfo, error) {
	p.asked = append(p.asked, "inspect")
	return p.info, p.inspErr
}
func (p *probes) OnScreen(id string) (string, bool) {
	p.asked = append(p.asked, "onscreen")
	return p.onScreen, p.onScreen != ""
}
func (p *probes) Guard(m tmuxpane.Mark) tmuxpane.GuardResult {
	p.asked = append(p.asked, "guard")
	return p.guard
}
func (p *probes) Gap(m tmuxpane.Mark) time.Duration { return p.gap }

var _ = Describe("tmux restore: the decision (CS-TMUX-051)", func() {
	const cid = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	var p *probes
	BeforeEach(func() {
		p = &probes{dirs: map[string]bool{"/srv/proj": true}, info: tmuxpane.ContainerInfo{
			State: "running", Name: "claude-sandbox-srv-proj-abc123-heron", Project: "/srv/proj", Instance: "heron"}}
	})
	row := func(mut func(*tmuxpane.Mark)) *tmuxpane.Row {
		empty := ""
		m := tmuxpane.Mark{V: 1, State: tmuxpane.StatePending, Mode: tmuxpane.ModeClaude, Container: "claude-sandbox-srv-proj-abc123-heron",
			ContainerID: cid, Instance: "heron", Project: "/srv/proj", ConfigDirEnv: &empty, Conversation: convID,
			Name: "fix the build", NameSource: "user"}
		if mut != nil {
			mut(&m)
		}
		return &tmuxpane.Row{Session: "main", Window: 1, Pane: 0, Mark: m}
	}
	decide := func(r *tmuxpane.Row) tmuxpane.Decision { return tmuxpane.Decide(r, "main:1.0", p) }

	It("CS-TMUX-051 row 2: no row is 'nothing', and no probe runs", func() {
		d := decide(nil)
		Expect(d.Row).To(Equal(2))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeNone))
		Expect(d.Line).To(Equal("nothing recorded for this pane (main:1.0) — the shell is yours"))
		Expect(p.asked).To(BeEmpty())
	})

	It("CS-TMUX-051 row 3: a row that fails the checks is cleared, naming the field, and nothing of it is printed", func() {
		d := decide(row(func(m *tmuxpane.Mark) { m.Project = "rel"; m.Name = "x" }))
		Expect(d.Row).To(Equal(3))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(ContainSubstring("its project is not valid"))
		Expect(d.Manual).To(BeEmpty())
		Expect(p.asked).To(BeEmpty())
	})

	It("CS-TMUX-051 rows 4 and 5: ralph prints its rerun command, a join is not restored; both clear", func() {
		d := decide(row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeRalph }))
		Expect(d.Row).To(Equal(4))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(Equal("a ralph run was here (/srv/proj); not restarted — run: cd /srv/proj && claude-sandbox --ralph"))
		d = decide(row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin }))
		Expect(d.Row).To(Equal(5))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(Equal("a joined session was here (fix the build); joins are not restored"))
		Expect(d.Manual).To(HavePrefix("cd /srv/proj && claude-sandbox --new"))
		d = decide(row(func(m *tmuxpane.Mark) { m.Mode = tmuxpane.ModeJoin; m.Name = "" }))
		Expect(d.Line).To(ContainSubstring("(" + convID + ")"))
		Expect(p.asked).To(BeEmpty())
	})

	It("CS-TMUX-051 rows 7 and 8: no project dir, or no docker, keep the row pending with the manual command", func() {
		d := decide(row(func(m *tmuxpane.Mark) { m.Project = "/gone" }))
		Expect(d.Row).To(Equal(7))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(d.Manual).To(ContainSubstring("--resume " + convID))
		p.dockerErr = errors.New("docker version failed")
		d = decide(row(nil))
		Expect(d.Row).To(Equal(8))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(d.Line).To(ContainSubstring("docker does not answer (docker version failed)"))
		Expect(d.Would()).To(ContainSubstring("keep the pane's mark pending"))
	})

	It("CS-TMUX-051 row 9: running with matching labels attaches by id; on screen elsewhere clears", func() {
		d := decide(row(nil))
		Expect(d.Row).To(Equal(9))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeAttach))
		Expect(d.Attach).To(Equal(cid))
		Expect(d.Would()).To(Equal("attach to the running container " + cid))
		Expect(p.asked).To(Equal([]string{"dir", "docker", "inspect", "onscreen"}))
		p.onScreen = "work:3.1"
		d = decide(row(nil))
		Expect(d.Row).To(Equal(9))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(Equal("'fix the build' is already on screen in work:3.1"))
	})

	It("CS-TMUX-051 rows 10..12: paused, restarting, and an inspect that cannot tell stay pending", func() {
		p.info.State = "paused"
		d := decide(row(nil))
		Expect(d.Row).To(Equal(10))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(d.Line).To(ContainSubstring("docker unpause claude-sandbox-srv-proj-abc123-heron"))
		p.info.State = "restarting"
		Expect(decide(row(nil)).Row).To(Equal(11))
		p.inspErr = errors.New("docker inspect: permission denied")
		d = decide(row(nil))
		Expect(d.Row).To(Equal(12))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(d.Line).To(ContainSubstring("cannot tell whether 'fix the build' still runs (docker inspect: permission denied)"))
	})

	It("CS-TMUX-051: gone is no id, not found, other labels, or created/exited/dead/removing — and then the guard decides", func() {
		for name, mut := range map[string]func(){
			"no id":          func() {},
			"not found":      func() { p.inspErr = tmuxpane.ErrNoContainer },
			"other project":  func() { p.info.Project = "/elsewhere" },
			"other instance": func() { p.info.Instance = "murre" },
			"created":        func() { p.info.State = "created" },
			"exited":         func() { p.info.State = "exited" },
			"dead":           func() { p.info.State = "dead" },
			"removing":       func() { p.info.State = "removing" },
		} {
			p = &probes{dirs: map[string]bool{"/srv/proj": true}, info: tmuxpane.ContainerInfo{State: "running", Project: "/srv/proj", Instance: "heron"}, gap: 10 * time.Second}
			mut()
			r := row(nil)
			if name == "no id" {
				r.Mark.ContainerID = ""
			}
			d := decide(r)
			Expect(d.Row).To(Equal(18), name)
			Expect(d.Outcome).To(Equal(tmuxpane.OutcomeResume), name)
			Expect(p.asked).To(ContainElement("guard"), name)
		}
	})

	It("CS-TMUX-051 rows 13 and 14: no conversation, or an unknown generated worktree, clear with what to do by hand", func() {
		p.info.State = "exited"
		d := decide(row(func(m *tmuxpane.Mark) { m.Conversation = ""; m.Worktree = "heron" }))
		Expect(d.Row).To(Equal(13))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(Equal("no conversation id was recorded for fix the build; pick it by hand: cd /srv/proj && claude-sandbox --new --worktree=heron -- --resume"))
		d = decide(row(func(m *tmuxpane.Mark) { m.WorktreeGenerated = true; m.Worktree = "" }))
		Expect(d.Row).To(Equal(14))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(p.asked).NotTo(ContainElement("guard"))
	})

	It("CS-TMUX-051 rows 15..17: the guard's holder or host claude clears; cannot tell stays pending", func() {
		p.info.State = "exited"
		p.guard = tmuxpane.GuardResult{Open: true, Holder: "'murre' (c2)", AttachCommand: "cd /srv/proj && claude-sandbox --attach=murre"}
		d := decide(row(nil))
		Expect(d.Row).To(Equal(15))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeClear))
		Expect(d.Line).To(Equal("'fix the build' is already running in 'murre' (c2) — attach: cd /srv/proj && claude-sandbox --attach=murre"))
		p.guard = tmuxpane.GuardResult{Open: true, HostPID: 4242}
		d = decide(row(nil))
		Expect(d.Row).To(Equal(16))
		Expect(d.Line).To(ContainSubstring("(pid 4242)"))
		p.guard = tmuxpane.GuardResult{Open: true, Reason: "docker ps failed"}
		d = decide(row(nil))
		Expect(d.Row).To(Equal(17))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomePending))
		Expect(d.Line).To(Equal("cannot tell whether " + convID + " is open elsewhere (docker ps failed)"))
	})

	It("CS-TMUX-051 row 18: resume, with the gap, the A7 notes and the manual command", func() {
		p.info.State = "exited"
		p.gap = tmuxpane.LegacyGap
		d := decide(row(func(m *tmuxpane.Mark) {
			m.Unreplayed = []string{"--docker-socket", "--add-dir"}
			m.FlagsUnknown = true
			m.ConfigDirEnv = nil
		}))
		Expect(d.Row).To(Equal(18))
		Expect(d.Outcome).To(Equal(tmuxpane.OutcomeResume))
		Expect(d.Gap).To(Equal(10 * time.Second))
		Expect(d.Would()).To(ContainSubstring("and 10 s more"))
		Expect(d.Notes).To(Equal([]string{
			"restored without flags given at launch: --docker-socket, --add-dir — relaunch by hand to use them",
			"the flags this session was launched with are unknown (it predates the launchflags label)",
			"its CLAUDE_CONFIG_DIR was not recorded (an older container): the restore uses this shell's",
		}))
		Expect(d.Manual).To(Equal("cd /srv/proj && claude-sandbox --new --no-worktree -- --resume " + convID + " --name 'fix the build'"))
		p.gap = 0
		Expect(decide(row(nil)).Would()).NotTo(ContainSubstring("more"))
	})

	Describe("CS-TMUX-051: the read-only probes", func() {
		var fake *execx.Fake
		BeforeEach(func() { fake = &execx.Fake{} })

		It("CS-TMUX-051: one bounded display-message reads the pane's coordinates, server and mark", func() {
			m := row(nil).Mark
			fake.On("tmux display-message", "main\t1\t0\t4100\t1790000000\t"+m.JSON()+"\n", nil)
			tp, ok := tmuxpane.ReadThisPane(fake, "%5")
			Expect(ok).To(BeTrue())
			Expect(tp.Coords()).To(Equal("main:1.0"))
			Expect(tp.Server).To(Equal(&tmuxpane.Server{PID: 4100, Start: 1790000000}))
			Expect(tp.Marked).To(BeTrue())
			Expect(tp.Mark.Conversation).To(Equal(convID))
			Expect(fake.CommandLines()).To(Equal([]string{"tmux display-message -p -t %5 #{session_name}\t#{window_index}\t#{pane_index}\t#{pid}\t#{start_time}\t#{@claude-sandbox}"}))
			fake2 := &execx.Fake{}
			fake2.On("tmux display-message", "", execx.Fail(1))
			_, ok = tmuxpane.ReadThisPane(fake2, "%5")
			Expect(ok).To(BeFalse())
		})

		It("CS-TMUX-049: the running server is one bounded display-message", func() {
			fake.On("tmux display-message -p #{pid}\t#{start_time}", "4100\t1790000000\n", nil)
			Expect(tmuxpane.RunningServer(fake)).To(Equal(&tmuxpane.Server{PID: 4100, Start: 1790000000}))
			fake2 := &execx.Fake{}
			fake2.On("tmux", "", execx.Fail(1))
			Expect(tmuxpane.RunningServer(fake2)).To(BeNil())
		})

		It("CS-TMUX-051: on screen means an active mark for the id in ANOTHER pane that runs the launcher; one list-panes", func() {
			on := row(nil).Mark
			on.State = tmuxpane.StateActive
			fake.On("tmux list-panes", strings.Join([]string{
				"%1\tmain\t1\t0\tclaude-sandbox\t" + on.JSON(), // self
				"%2\twork\t2\t0\tzsh\t" + on.JSON(),            // a stale mark under a shell
				"%3\twork\t3\t1\tclaude-sandbox\t" + on.JSON(),
			}, "\n")+"\n", nil)
			rp := &tmuxpane.ReadProbes{Runner: fake, Self: "%1"}
			where, ok := rp.OnScreen(cid)
			Expect(ok).To(BeTrue())
			Expect(where).To(Equal("work:3.1"))
			_, ok = rp.OnScreen(strings.Repeat("0", 64))
			Expect(ok).To(BeFalse())
			Expect(fake.CommandLines()).To(Equal([]string{"tmux list-panes -a -F #{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_current_command}\t#{@claude-sandbox}"}))
		})

		It("CS-TMUX-051: docker is asked once with a bounded docker version", func() {
			fake.On("docker version", "29.3.0\n", nil)
			rp := &tmuxpane.ReadProbes{Runner: fake}
			Expect(rp.Docker()).To(Succeed())
			Expect(rp.Docker()).To(Succeed())
			Expect(fake.CommandLines()).To(Equal([]string{"docker version --format {{.Server.Version}}"}))
			fake2 := &execx.Fake{}
			fake2.On("docker version", "", execx.Fail(1))
			Expect((&tmuxpane.ReadProbes{Runner: fake2}).Docker()).To(HaveOccurred())
		})

		It("CS-TMUX-051: the inspect maps docker's 'no such container' to gone, anything else to cannot tell", func() {
			fake.OnFunc("docker inspect", func(c execx.Cmd) (string, error) {
				c.Stderr.Write([]byte("Error: No such container: " + cid + "\n"))
				return "", execx.Fail(1)
			})
			_, err := (&tmuxpane.ReadProbes{Runner: fake}).Inspect(cid)
			Expect(err).To(MatchError(tmuxpane.ErrNoContainer))
			fake2 := &execx.Fake{}
			fake2.On("docker inspect", "/claude-sandbox-x\x1frunning\x1f2026-09-29T12:00:00.1Z\x1f/srv/proj\x1fheron\x1fclaude\n", nil)
			info, err := (&tmuxpane.ReadProbes{Runner: fake2}).Inspect(cid)
			Expect(err).NotTo(HaveOccurred())
			Expect(info).To(Equal(tmuxpane.ContainerInfo{State: "running", Name: "claude-sandbox-x", Project: "/srv/proj", Instance: "heron"}))
		})

		It("CS-TMUX-051: the gap is 0 on a linked or relocated layout and 10 s otherwise", func() {
			home := GinkgoT().TempDir()
			m := row(nil).Mark
			Expect(tmuxpane.GapFor(home, m, "")).To(Equal(tmuxpane.LegacyGap), "missing")
			Expect(os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600)).To(Succeed())
			Expect(tmuxpane.GapFor(home, m, "")).To(Equal(tmuxpane.LegacyGap), "legacy")
			Expect(os.Remove(filepath.Join(home, ".claude.json"))).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(home, ".claude"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(home, ".claude", ".claude.json"), []byte("{}"), 0o600)).To(Succeed())
			Expect(os.Symlink(".claude/.claude.json", filepath.Join(home, ".claude.json"))).To(Succeed())
			Expect(tmuxpane.GapFor(home, m, "")).To(BeZero(), "linked")
			cde := "/cfg"
			m.ConfigDirEnv = &cde
			Expect(tmuxpane.GapFor(GinkgoT().TempDir(), m, "")).To(BeZero(), "relocated, from the row")
			m.ConfigDirEnv = nil
			Expect(tmuxpane.GapFor(GinkgoT().TempDir(), m, "/cfg")).To(BeZero(), "relocated, the shell's for an older row")
		})
	})
})
