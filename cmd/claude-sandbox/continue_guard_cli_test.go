package main

// Spec: spec/launch.feature CS-LNCH-182 and spec/sessions.feature
// CS-SESS-093..096 — the continue label on docker create, and the continue
// guard: before the image work (records only), under the launch lock before
// the create, before a join's exec, the per-path messages and the tier-1 note.
// The decision itself (rules a'/b'/h', T, the config-dir rule) is covered in
// internal/resumeguard/continue_test.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// contRow is a 25-field discovery row: every label through the continue one,
// Mounts last.
type contRow struct {
	name, project, mode, instance, class, state, hash, registry, resume string
	created                                                             time.Time
	cont                                                                bool
}

func (r contRow) String() string {
	status := "Up 1 hour"
	if r.state == "created" {
		status = "Created"
	}
	mode := r.mode
	if mode == "" {
		mode = "claude"
	}
	c := ""
	if r.cont {
		c = "1"
	}
	inputs := ""
	if r.hash != "" {
		inputs = "[]"
	}
	return strings.Join([]string{r.name, status, r.project, mode, r.instance, "v1", "", r.hash, inputs, r.class, "",
		r.state, r.created.Format("2006-01-02 15:04:05 -0700 MST"), "8g", "default", "",
		strings.Repeat("a", 64), "", r.registry, "", r.resume, "none", "none", c, ""}, psSep)
}

// allFilter is the docker ps of a host-wide discovery (the pre-build check,
// the reservation, a join's check), as opposed to the project's own.
func allFilter(c execx.Cmd) bool {
	return strings.Contains(strings.Join(c.Args, " "), "--filter label=claude-sandbox.project --filter")
}

var _ = Describe("the continue guard (CS-LNCH-182, CS-SESS-093..096)", func() {
	const heldID = "53cd0872-ec39-41a3-86bd-b000abb5fb32"
	var f *cliFixture
	var reg string
	BeforeEach(func() {
		f = newCLIFixture()
		reg = filepath.Join(f.home, ".cache", "claude-sandbox", "peers", "sessions")
	})
	// holder makes 'email' a running sandbox of the project whose record at
	// class 7 (a join, 263) has heldID open in cwd. No procStart: alive with
	// no docker top (CS-SESS-089).
	holder := func(g *cliFixture, cwd string) string {
		r := filepath.Join(g.home, ".cache", "claude-sandbox", "peers", "sessions")
		cwdJSON, err := json.Marshal(cwd) // a JSON string: control characters as \u escapes
		Expect(err).NotTo(HaveOccurred())
		writeFile(filepath.Join(r, "263.json"), fmt.Sprintf(`{"pid":263,"sessionId":%q,"cwd":%s,"startedAt":%d}`,
			heldID, cwdJSON, time.Now().Add(time.Hour).UnixMilli()))
		return contRow{name: "cs-proj-email", project: g.proj, instance: "email", class: "7", state: "running",
			created: time.Now(), registry: r}.String() + "\n"
	}
	head := func(cwd string) string {
		return "Error: --continue would reopen the newest conversation in " + cwd + ",\n" +
			"       and 'email' (cs-proj-email) has conversation " + heldID + " open there;\n" +
			"       a second session on one conversation would interleave writes into one transcript.\n"
	}

	Describe("CS-LNCH-182: the continue label", func() {
		It("CS-LNCH-182: a guarded continue labels the create, outside the fingerprint; headless too", func() {
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.run("--new", "--", "--continue")).To(Equal(0), f.errw.String())
			all := creates(f)
			Expect(all).To(HaveLen(2))
			plain, cont := labelsOf(all[0]), labelsOf(all[1])
			Expect(plain).NotTo(HaveKey("claude-sandbox.continue"))
			Expect(cont).To(HaveKeyWithValue("claude-sandbox.continue", "1"))
			Expect(cont["claude-sandbox.confighash"]).To(Equal(plain["claude-sandbox.confighash"]))
			Expect(cont["claude-sandbox.inputs"]).To(Equal(plain["claude-sandbox.inputs"]))

			for _, args := range [][]string{
				{"--new", "--", "-c"}, {"--new", "--", "-pc"}, {"--new", "--", "fix it", "--continue"},
				{"--new", "--", "--append-system-prompt", "--continue"},
				{"--new", "--", "--add-dir", "--fork-session", "--continue"},
				{"headless", "--", "--continue"},
			} {
				g := newCLIFixture()
				Expect(g.run(args...)).To(Equal(0), "%v: %s", args, g.errw.String())
				Expect(labelsOf(g.launched().Args)).To(HaveKeyWithValue("claude-sandbox.continue", "1"), "%v", args)
			}
		})

		It("CS-LNCH-182: --resume <id> --continue gets both labels", func() {
			Expect(f.run("--new", "--", "--resume", guardID, "--continue")).To(Equal(0), f.errw.String())
			l := labelsOf(f.launched().Args)
			Expect(l).To(HaveKeyWithValue("claude-sandbox.resume", guardID))
			Expect(l).To(HaveKeyWithValue("claude-sandbox.continue", "1"))
		})

		It("CS-LNCH-182: a fork, a stopping --, -rc, --branch, the tier-1 [b] and ralph carry no continue label", func() {
			for _, args := range [][]string{
				{"--new", "--", "--continue", "--fork-session"},
				{"--new", "--", "--fork-session", "--continue"},
				{"--new", "--", "--", "--continue"},
				{"--new", "--", "-rc"},
				{"--branch"},
				{"--ralph"},
				{"--ralph", "--", "--continue"},
			} {
				g := newCLIFixture()
				Expect(g.run(args...)).To(Equal(0), "%v: %s", args, g.errw.String())
				Expect(labelsOf(g.launched().Args)).NotTo(HaveKey("claude-sandbox.continue"), "%v", args)
			}
		})

		It("CS-LNCH-182: --branch and the tier-1 [b] fork past a holder, and never read a registry for it", func() {
			row := holder(f, f.proj)
			f.fake.On("docker ps", row, nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"b"}}
			Expect(f.run("--", "--continue")).To(Equal(0), f.errw.String())
			Expect(f.launchLine()).To(ContainSubstring(" claude --continue --fork-session --continue"))
			Expect(labelsOf(f.launched().Args)).NotTo(HaveKey("claude-sandbox.continue"))

			g := newCLIFixture()
			g.fake.On("docker ps", holder(g, g.proj), nil)
			Expect(g.run("--branch")).To(Equal(0), g.errw.String())
			Expect(creates(g)).To(HaveLen(1))
		})
	})

	Describe("CS-SESS-093: refusal", func() {
		It("CS-SESS-093: a live record in the launch's directory refuses with exit 4, before any image work, creating nothing", func() {
			f.fake.On("docker ps", holder(f, f.proj), nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			Expect(creates(f)).To(BeEmpty())
			Expect(f.fake.Session).To(BeNil())
			Expect(f.errw.String()).To(ContainSubstring(head(f.proj) +
				"       Attach to it:  cd " + f.proj + " && claude-sandbox --attach=email\n" +
				"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
				"       Or pick one:   use --resume instead of --continue (claude's picker; " + heldID + " is the one already open)"))
			// CS-SESS-094: the pre-build check refused: no lock, no image work.
			Expect(f.lock.acquiredAt).To(BeEmpty())
			for _, l := range f.fake.CommandLines() {
				Expect(l).NotTo(HavePrefix("docker build"), "no image work")
				Expect(l).NotTo(HavePrefix("docker buildx"), "no image work")
				Expect(l).NotTo(HavePrefix("docker image"), "no image work")
			}
		})

		It("CS-SESS-093: --detach and headless name a placeholder, never the open id, and no picker", func() {
			f.fake.On("docker ps", holder(f, f.proj), nil)
			Expect(f.run("--detach", "--", "--continue")).To(Equal(4))
			Expect(f.errw.String()).To(ContainSubstring(head(f.proj) +
				"       Attach to it:  cd " + f.proj + " && claude-sandbox --attach=email\n" +
				"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
				"       Or name one:   pass --resume=<conversation-id> after -- for the conversation you want (" +
				heldID + " is the one already open; a picker would wait in a session nobody is attached to)"))
			Expect(f.errw.String()).NotTo(ContainSubstring("Or pick one"))

			g := newCLIFixture()
			g.fake.On("docker ps", holder(g, g.proj), nil)
			Expect(g.run("headless", "--", "--continue")).To(Equal(4))
			Expect(g.out.String()).To(BeEmpty(), "headless stdout stays claude's")
			msg := g.errw.String()
			Expect(msg).To(ContainSubstring(head(g.proj) +
				"       Or fork it:    pass --fork-session\n" +
				"       Or name one:   pass --resume <conversation-id> for the conversation you want (" + heldID + " is the one already open)"))
			Expect(msg).NotTo(ContainSubstring("Attach to it"))
			Expect(msg).NotTo(ContainSubstring("Or pick one"))
			Expect(creates(g)).To(BeEmpty())
		})

		It("CS-SESS-093: a record's cwd carrying ESC and BEL is matched raw but printed without control characters", func() {
			// filepath.Clean folds the trailing "/..", so the record still
			// matches the launch directory.
			evil := f.proj + "/\u001b]0;PWNED\u0007\u001b[2J/.."
			f.fake.On("docker ps", holder(f, evil), nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			msg := f.errw.String()
			Expect(msg).To(ContainSubstring("Error: --continue would reopen the newest conversation in "))
			for _, r := range msg {
				if r != '\n' {
					Expect(r >= 0x20 && r != 0x7f).To(BeTrue(), "control character %U in %q", r, msg)
				}
			}
			Expect(msg).To(ContainSubstring("]0;PWNED [2J/.."), "the rest of the cwd stays readable")
		})

		It("CS-SESS-094: a reason carrying control characters is printed cleaned", func() {
			bad := filepath.Join(f.home, "reg\u001b[2Jdir")
			writeFile(bad, "not a directory")
			row := contRow{name: "cs-proj-email", project: f.proj, instance: "email", class: "7", state: "running",
				created: time.Now(), registry: bad}.String() + "\n"
			f.fake.On("docker ps", row, nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			msg := f.errw.String()
			Expect(msg).To(ContainSubstring("Error: cannot tell whether a conversation in "))
			Expect(msg).NotTo(ContainSubstring("\u001b"))
		})

		It("CS-SESS-093: a holder elsewhere does not refuse; --fork-session is the way out", func() {
			f.fake.On("docker ps", holder(f, "/some/other/dir"), nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(0), f.errw.String())

			g := newCLIFixture()
			g.fake.On("docker ps", holder(g, g.proj), nil)
			Expect(g.run("--new", "--", "--continue", "--fork-session")).To(Equal(0), g.errw.String())
			Expect(creates(g)).To(HaveLen(1))
		})

		It("CS-SESS-093: a record in the launch's named worktree refuses; T is settled under the lock", func() {
			f.fake.On("rev-parse --show-toplevel", f.proj+"\n", nil)
			wtDir := filepath.Join(f.proj, ".claude", "worktrees", "feat")
			row := holder(f, filepath.Join(wtDir, "pkg"))
			f.fake.On("docker ps", row, nil)
			Expect(f.run("--new", "--worktree=feat", "--", "--continue")).To(Equal(4), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("newest conversation in " + filepath.Join(wtDir, "pkg") + ","))

			// The passthrough's own -w names count too, every one of them.
			g := newCLIFixture()
			g.fake.On("rev-parse --show-toplevel", g.proj+"\n", nil)
			g.fake.On("docker ps", holder(g, filepath.Join(g.proj, ".claude", "worktrees", "b")), nil)
			Expect(g.run("--new", "--", "-w", "a", "--newbool", "-w", "b", "--continue")).To(Equal(4), g.errw.String())
		})

		It("CS-SESS-093: a reservation of the project still starting refuses only under the lock", func() {
			res := contRow{name: "cs-proj-otter", project: f.proj, instance: "otter", class: "9", state: "created",
				created: time.Now(), registry: reg, cont: true}.String() + "\n"
			f.fake.On("docker ps", res, nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			Expect(f.lock.acquiredAt).NotTo(BeEmpty(), "the pre-build check never refuses on a reservation")
			Expect(creates(f)).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring(
				"Error: --continue would reopen the newest conversation in " + f.proj + ",\n" +
					"       and 'otter' (cs-proj-otter) is starting a session there that opens a conversation;\n" +
					"       a second session on one conversation would interleave writes into one transcript.\n" +
					"       Retry once it is up.\n" +
					"       Attach to it:  cd " + f.proj + " && claude-sandbox --attach=otter\n" +
					"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
					"       Or pick one:   use --resume instead of --continue (claude's picker)"))
		})

		It("CS-SESS-094: an orphaned reservation (older than 60 s) is removed under the lock and the launch goes ahead", func() {
			res := contRow{name: "cs-proj-otter", project: f.proj, instance: "otter", class: "9", state: "created",
				created: time.Now().Add(-2 * time.Minute), registry: reg, cont: true}.String() + "\n"
			f.fake.On("docker ps", res, nil)
			Expect(f.run("--new", "--", "--continue")).To(Equal(0), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("Removed stale reservation cs-proj-otter"))
			Expect(creates(f)).To(HaveLen(1))
		})
	})

	Describe("CS-SESS-094: the host, failing closed, the lock", func() {
		It("CS-SESS-094: a live claude on the host in the directory refuses with exit 4, without an Attach line", func() {
			proc := filepath.Join(f.home, "proc")
			Expect(os.MkdirAll(filepath.Join(proc, "self", "ns"), 0o755)).To(Succeed())
			Expect(os.Symlink("pid:[42]", filepath.Join(proc, "self", "ns", "pid"))).To(Succeed())
			writeFile(filepath.Join(proc, "4242", "stat"), "4242 (claude) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 1\n")
			writeFile(filepath.Join(f.home, ".claude", "sessions", "4242.json"),
				fmt.Sprintf(`{"pid":4242,"sessionId":%q,"cwd":%q,"startedAt":1,"procStart":"777","pidDomain":"linux::pid:[42]"}`, heldID, f.proj))
			f.env.ProcRoot = proc
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			Expect(creates(f)).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring(
				"       and a claude process on this host (pid 4242) has conversation " + heldID + " open there;\n"))
			Expect(f.errw.String()).NotTo(ContainSubstring("Attach to it"))
		})

		It("CS-SESS-094: a failed discovery under the lock refuses; at pre-build it is left to the lock", func() {
			f.fake.OnFunc("docker ps", func(c execx.Cmd) (string, error) {
				if allFilter(c) {
					return "", execx.Fail(1) // pre-build and under the lock
				}
				return "", nil
			})
			Expect(f.run("--new", "--", "--continue")).To(Equal(4))
			Expect(f.lock.acquiredAt).NotTo(BeEmpty(), "pre-build did not refuse")
			Expect(creates(f)).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring(
				"Error: cannot tell whether a conversation in " + f.proj + " is already open elsewhere\n" +
					"       (listing sandbox containers: exit 1), so --continue is not run.\n" +
					"       Fix that and retry.\n" +
					"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
					"       Or pick one:   use --resume instead of --continue (claude's picker)"))

			// Failing at pre-build only: the launch goes on to the lock and creates.
			g := newCLIFixture()
			g.fake.OnFunc("docker ps", func(c execx.Cmd) (string, error) {
				if allFilter(c) && len(g.lock.acquiredAt) == 0 {
					return "", execx.Fail(1)
				}
				return "", nil
			})
			Expect(g.run("--new", "--", "--continue")).To(Equal(0), g.errw.String())
			Expect(creates(g)).To(HaveLen(1))
		})

		It("CS-SESS-094: a continue never launches without the lock; a plain launch still does", func() {
			f.lock.err = errors.New("timed out after 30s")
			Expect(f.run("--new", "--", "--continue")).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring(
				"Error: could not take the launch lock (timed out after 30s); not continuing in " + f.proj + " unserialized."))
			Expect(creates(f)).To(BeEmpty())

			g := newCLIFixture()
			g.lock.err = errors.New("timed out after 30s")
			Expect(g.run("--new", "--", "--resume", guardID, "--continue")).To(Equal(2))
			Expect(g.errw.String()).To(ContainSubstring("not resuming " + guardID + " unserialized."))
		})

		It("CS-SESS-094: a launch that is not a guarded continue runs no pre-build discovery and is not refused by a holder", func() {
			f.fake.On("docker ps", holder(f, f.proj), nil)
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			n := 0
			for _, c := range f.fake.Calls {
				if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "ps" && allFilter(c) {
					n++
				}
			}
			Expect(n).To(Equal(1), "only the reservation's own discovery")
		})
	})

	Describe("CS-SESS-095: joins", func() {
		target := func(g *cliFixture) string {
			r := filepath.Join(g.home, ".cache", "claude-sandbox", "peers", "sessions")
			writeFile(filepath.Join(r, "7.json"), fmt.Sprintf(`{"pid":7,"sessionId":%q,"cwd":%q,"startedAt":%d}`,
				heldID, g.proj, time.Now().Add(time.Hour).UnixMilli()))
			return contRow{name: "cs-proj-email", project: g.proj, instance: "email", class: "7", state: "running",
				created: time.Now(), registry: r, hash: currentHash(g)}.String() + "\n"
		}

		It("CS-SESS-095: --join with --continue into a container whose primary has the directory's conversation open refuses before the exec", func() {
			f.fake.On("docker ps", target(f), nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--join=email", "--", "--continue")).To(Equal(4), f.errw.String())
			Expect(f.fake.Session).To(BeNil(), "no docker exec")
			for _, l := range f.fake.CommandLines() {
				Expect(l).NotTo(HavePrefix("docker exec"))
			}
			Expect(f.errw.String()).To(ContainSubstring(head(f.proj) +
				"       Attach to it:  cd " + f.proj + " && claude-sandbox --attach=email\n" +
				"       Or fork it:    add --fork-session after -- (a new conversation id)\n" +
				"       Or pick one:   use --resume instead of --continue (claude's picker; " + heldID + " is the one already open)"))
		})

		It("CS-SESS-095: the tier-1 [j] is checked the same way; a join without --continue runs no check", func() {
			f.fake.On("docker ps", target(f), nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"j"}}
			Expect(f.run("--", "--continue")).To(Equal(4), f.errw.String())
			Expect(f.fake.Session).To(BeNil())

			g := newCLIFixture()
			g.fake.On("docker ps", target(g), nil)
			g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(g.run("--join=email", "--", "--verbose")).To(Equal(0), g.errw.String())
			Expect(g.sessionLine()).To(HavePrefix("docker exec"))
		})

		It("CS-SESS-095: a young continue reservation (under 60 s) of the project refuses a join", func() {
			r := filepath.Join(f.home, "reg-empty")
			rows := contRow{name: "cs-proj-email", project: f.proj, instance: "email", class: "7", state: "running",
				created: time.Now(), registry: r, hash: currentHash(f)}.String() + "\n" +
				contRow{name: "cs-proj-otter", project: f.proj, instance: "otter", class: "9", state: "created",
					created: time.Now().Add(-10 * time.Second), registry: r, cont: true}.String() + "\n"
			f.fake.On("docker ps", rows, nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--join=email", "--", "--continue")).To(Equal(4), f.errw.String())
			Expect(f.errw.String()).To(ContainSubstring("and 'otter' (cs-proj-otter) is starting a session there"))
			Expect(f.fake.Session).To(BeNil())
		})

		It("CS-SESS-095: an orphaned continue reservation (older than 60 s) does not refuse a join", func() {
			r := filepath.Join(f.home, "reg-empty")
			rows := contRow{name: "cs-proj-email", project: f.proj, instance: "email", class: "7", state: "running",
				created: time.Now(), registry: r, hash: currentHash(f)}.String() + "\n" +
				contRow{name: "cs-proj-otter", project: f.proj, instance: "otter", class: "9", state: "created",
					created: time.Now().Add(-2 * time.Minute), registry: r, cont: true}.String() + "\n"
			f.fake.On("docker ps", rows, nil)
			f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
			Expect(f.run("--join=email", "--", "--continue")).To(Equal(0), f.errw.String())
			Expect(f.sessionLine()).To(HavePrefix("docker exec"))
		})
	})

	It("CS-SESS-096: the tier-1 prompt notes that --continue may be refused, under the report and before the menu", func() {
		f.fake.On("docker ps", contRow{name: "cs-proj-email", project: f.proj, instance: "email", class: "7",
			state: "running", created: time.Now(), registry: reg}.String()+"\n", nil)
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
		Expect(f.run("--", "--continue")).To(Equal(0))
		msg := f.errw.String()
		note := "Note: --continue reopens the newest conversation in this directory, which a session above may have open; [n] and [j] are refused while one does — [b] forks it instead.\n"
		Expect(msg).To(ContainSubstring(note))
		Expect(strings.Index(msg, "Found 1 running session(s)")).To(BeNumerically("<", strings.Index(msg, note)))
		Expect(strings.Index(msg, note)).To(BeNumerically("<", strings.Index(msg, "  [n] new session")))

		g := newCLIFixture()
		g.fake.On("docker ps", contRow{name: "cs-proj-email", project: g.proj, instance: "email", class: "7",
			state: "running", created: time.Now(), registry: reg}.String()+"\n", nil)
		g.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
		g.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
		Expect(g.run("--", "--continue", "--fork-session")).To(Equal(0))
		Expect(g.errw.String()).NotTo(ContainSubstring("Note: --continue"))
	})
})
