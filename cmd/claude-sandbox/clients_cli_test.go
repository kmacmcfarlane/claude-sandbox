package main

// Spec: spec/launch.feature CS-LNCH-204 (every session client holds a lease)
// and spec/host-dirs.feature CS-DIR-029 (the prune under the launch lock).
// Leases go to the fixture's scratch Env.StateDir; the reader is
// internal/clients, tested there (CS-SESS-098).

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/clients"
	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions/sessionstest"
)

// heldKinds lists the kinds of the held leases on container id.
func heldKinds(state, id string) []clients.Kind {
	ls, err := clients.ReadLeases(state, id)
	Expect(err).NotTo(HaveOccurred())
	var out []clients.Kind
	for _, l := range ls {
		if l.Held {
			out = append(out, l.Record.Kind)
		}
	}
	return out
}

// promptHook is a Scripted prompter that runs check before each answer.
type promptHook struct {
	*prompt.Scripted
	check func()
}

func (p promptHook) Ask(pre, q string, t time.Duration) string {
	p.check()
	return p.Scripted.Ask(pre, q, t)
}

var _ = Describe("client leases (CS-LNCH-204)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	// during records the held lease kinds of markID while the session child
	// whose argv contains pattern runs.
	during := func(pattern string) *[]clients.Kind {
		var seen []clients.Kind
		f.fake.OnFunc(pattern, func(execx.Cmd) (string, error) {
			seen = heldKinds(f.state, markID)
			return "", nil
		})
		return &seen
	}

	It("CS-LNCH-204: a new container holds a primary lease from its create through the session, released after", func() {
		f.fake.On("docker create", markID+"\n", nil)
		seen := during("docker start -ai")
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(*seen).To(Equal([]clients.Kind{clients.KindPrimary}))
		Expect(heldKinds(f.state, markID)).To(BeEmpty())
		ents, _ := os.ReadDir(clients.ContainerDir(f.state, markID))
		Expect(ents).To(BeEmpty(), "released = unlinked")
	})

	It("CS-LNCH-204: a lease records the tmux pane and the launch time", func() {
		now := time.UnixMilli(1_790_000_000_123)
		f.env.Now = func() time.Time { return now }
		f.envmap["TMUX"] = "/tmp/tmux-1000/default,1,0"
		f.envmap["TMUX_PANE"] = "%9"
		f.fake.On("docker create", markID+"\n", nil)
		var rec clients.Record
		f.fake.OnFunc("docker start -ai", func(execx.Cmd) (string, error) {
			ls, _ := clients.ReadLeases(f.state, markID)
			if len(ls) == 1 {
				rec = ls[0].Record
			}
			return "", nil
		})
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(rec).To(Equal(clients.Record{V: 1, Kind: clients.KindPrimary, Since: now.UnixMilli(), TmuxPane: "%9"}))
	})

	It("CS-LNCH-204: a failed start still releases the lease", func() {
		f.fake.On("docker create", markID+"\n", nil)
		f.fake.On("docker start -ai", "", errors.New("exec: docker: not found"))
		Expect(f.run()).NotTo(Equal(0))
		Expect(heldKinds(f.state, markID)).To(BeEmpty())
	})

	It("CS-LNCH-204: a headless session's lease is kind sdk", func() {
		f.fake.On("docker create", markID+"\n", nil)
		seen := during("docker start -ai")
		Expect(f.run("headless", "--")).To(Equal(0), f.errw.String())
		Expect(*seen).To(Equal([]clients.Kind{clients.KindSDK}))
	})

	It("CS-LNCH-204: --detach takes no lease", func() {
		f.fake.On("docker create", markID+"\n", nil)
		f.fake.On("docker inspect --type container", "running 2026-10-22T00:00:00Z\n", nil)
		f.fake.On("docker events", "", nil)
		Expect(f.run("--detach")).To(Equal(0), f.errw.String())
		Expect(f.out.String()).To(ContainSubstring("in the background"))
		_, err := os.Lstat(filepath.Join(f.state, "clients"))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue(), f.errw.String())
	})

	It("CS-LNCH-204: inside a sandbox nothing is written", func() {
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		f.envmap["TMPDIR"] = f.home
		f.fake.On("docker create", markID+"\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		_, err := os.Lstat(filepath.Join(f.state, "clients"))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
	})

	It("CS-LNCH-204: a create that printed no 64-hex id gets no lease, and the launch goes on", func() {
		Expect(f.run()).To(Equal(0), f.errw.String())
		_, err := os.Lstat(filepath.Join(f.state, "clients"))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
	})

	It("CS-LNCH-204: an unusable state root skips the lease silently", func() {
		Expect(os.RemoveAll(f.state)).To(Succeed())
		Expect(os.WriteFile(f.state, nil, 0o600)).To(Succeed()) // a file in the way
		f.fake.On("docker create", markID+"\n", nil)
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(f.errw.String()).NotTo(ContainSubstring("clients"))
	})

	Describe("attach and join", func() {
		created := "2026-09-29 11:00:00 +0000 UTC"
		running := func(row string) {
			f.fake.On("docker ps", row+"\n", nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		}
		otter := func() string {
			return psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "", created, markID, "", "", "")
		}

		It("CS-LNCH-204: an attach holds an attach lease during the session, released after", func() {
			running(otter())
			seen := during("docker attach")
			Expect(f.run("--attach=otter")).To(Equal(0), f.errw.String())
			Expect(*seen).To(Equal([]clients.Kind{clients.KindAttach}))
			Expect(heldKinds(f.state, markID)).To(BeEmpty())
		})

		It("CS-LNCH-204: a join holds a join lease during the session, released after", func() {
			running(otter())
			seen := during("docker exec")
			Expect(f.run("--join=otter")).To(Equal(0), f.errw.String())
			Expect(*seen).To(Equal([]clients.Kind{clients.KindJoin}))
			Expect(heldKinds(f.state, markID)).To(BeEmpty())
		})

		It("CS-LNCH-204: the lease is held at the drift prompt, and a quit there releases it", func() {
			row := sessionstest.Row("cs-otter", "Up 1 hour", f.proj, "claude", "otter", "v1", "", "stalehash1234", "[]", "37", "",
				"running", created, "8g", "default", "", markID, "", "", "")
			running(row)
			var atPrompt []clients.Kind
			f.env.Prompter = promptHook{
				Scripted: &prompt.Scripted{IsTTY: true, Answers: []string{"q"}},
				check:    func() { atPrompt = heldKinds(f.state, markID) },
			}
			f.run("--attach=otter")
			Expect(f.errw.String()).To(ContainSubstring("different configuration"))
			Expect(atPrompt).To(Equal([]clients.Kind{clients.KindAttach}))
			Expect(f.fake.Session).To(BeNil())
			Expect(heldKinds(f.state, markID)).To(BeEmpty())
		})

		It("CS-LNCH-204: the tier-1 [a] takes the lease once the container is chosen", func() {
			running(otter())
			seen := during("docker attach")
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"a"}}
			Expect(f.run()).To(Equal(0), f.errw.String())
			Expect(*seen).To(Equal([]clients.Kind{clients.KindAttach}))
		})
	})
})

var _ = Describe("client directory prune (CS-DIR-029)", func() {
	var f *cliFixture
	const other = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
	BeforeEach(func() { f = newCLIFixture() })

	dead := func(id string) string {
		dir := clients.ContainerDir(f.state, id)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		p := filepath.Join(dir, "1.0000000000000000.lease")
		Expect(os.WriteFile(p, []byte(`{"v":1,"kind":"attach","since":1}`), 0o600)).To(Succeed())
		return p
	}

	It("CS-DIR-029: a launch removes the directories of containers discovery no longer lists, and dead leases of listed ones", func() {
		gone := dead(other)
		listed := dead(markID)
		f.fake.On("docker ps", psRowMark("cs-otter", f.proj, "claude", "otter", "", "37", "",
			"2026-09-29 11:00:00 +0000 UTC", markID, "", "", "")+"\n", nil)
		Expect(f.run("--new")).To(Equal(0), f.errw.String())
		_, err := os.Lstat(filepath.Dir(gone))
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		_, err = os.Lstat(listed)
		Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
		Expect(filepath.Dir(listed)).To(BeADirectory())
	})

	It("CS-DIR-029: a failed discovery removes nothing, and the launch goes on", func() {
		gone := dead(other)
		f.fake.On("docker ps", "", errors.New("Cannot connect to the Docker daemon"))
		Expect(f.run("--no-session-check")).To(Equal(0), f.errw.String())
		Expect(gone).To(BeAnExistingFile())
	})

	It("CS-DIR-029: inside a sandbox nothing is pruned", func() {
		gone := dead(other)
		f.envmap["CLAUDE_SANDBOX_PROJECT_DIR"] = f.proj
		f.envmap["TMPDIR"] = f.home
		Expect(f.run()).To(Equal(0), f.errw.String())
		Expect(gone).To(BeAnExistingFile())
	})
})
