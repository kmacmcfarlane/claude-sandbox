package main

// Spec: spec/launch.feature CS-LNCH-110 and spec/sessions.feature
// CS-SESS-065..069 — the resume label on docker create, and the guard that
// refuses, inside the launch lock, a launch resuming a conversation already
// open elsewhere. The decision itself (rules a/b, hardened reads, the host
// claude check) is covered in internal/resumeguard.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

const guardID = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"

// psRowResume is a discovery row carrying the registry and resume labels.
func psRowResume(name, project, mode, instance, class, state string, created time.Time, registry, resume string) string {
	status := "Up 1 hour"
	if state == "created" {
		status = "Created"
	}
	return strings.Join([]string{name, status, project, mode, instance, "v1", "", "", "", class, "",
		state, created.Format("2006-01-02 15:04:05 -0700 MST"), "8g", "default", "",
		strings.Repeat("a", 64), "", registry, "", resume}, psSep)
}

// labelsOf returns the --label values of a docker create argv.
func labelsOf(args []string) map[string]string {
	out := map[string]string{}
	for i, a := range args {
		if a == "--label" && i+1 < len(args) {
			k, v, _ := strings.Cut(args[i+1], "=")
			out[k] = v
		}
	}
	return out
}

// creates returns every docker create argv recorded.
func creates(f *cliFixture) [][]string {
	var out [][]string
	for _, c := range f.fake.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "create" {
			out = append(out, c.Args)
		}
	}
	return out
}

var _ = Describe("the resume guard (CS-LNCH-110, CS-SESS-065..069)", func() {
	var f *cliFixture
	BeforeEach(func() { f = newCLIFixture() })

	Describe("CS-LNCH-110: the resume label", func() {
		It("CS-LNCH-110: an explicit resume of a UUID labels the create, in lower case, outside the fingerprint", func() {
			Expect(f.run("--new")).To(Equal(0), f.errw.String())
			Expect(f.run("--new", "--", "--resume", strings.ToUpper(guardID))).To(Equal(0), f.errw.String())
			all := creates(f)
			Expect(all).To(HaveLen(2))
			plain, resumed := labelsOf(all[0]), labelsOf(all[1])
			Expect(plain).NotTo(HaveKey("claude-sandbox.resume"))
			Expect(resumed).To(HaveKeyWithValue("claude-sandbox.resume", guardID))
			Expect(resumed["claude-sandbox.confighash"]).To(Equal(plain["claude-sandbox.confighash"]))
			Expect(resumed["claude-sandbox.inputs"]).To(Equal(plain["claude-sandbox.inputs"]))
		})

		It("CS-LNCH-110: every spelling labels; a fork, the picker, a name, --branch, headless and ralph do not", func() {
			for _, pt := range [][]string{
				{"--resume=" + guardID}, {"-r", guardID}, {"-r" + guardID},
			} {
				g := newCLIFixture()
				Expect(g.run(append([]string{"--new", "--"}, pt...)...)).To(Equal(0), g.errw.String())
				Expect(labelsOf(g.launched().Args)).To(HaveKeyWithValue("claude-sandbox.resume", guardID), "%v", pt)
			}
			for _, args := range [][]string{
				{"--new", "--", "--resume", guardID, "--fork-session"},
				{"--new", "--", "--resume"},
				{"--new", "--", "--resume", "my-session"},
				{"--new", "--", "fix", "--resume", guardID},
				{"--branch"},
				{"headless", "--", "--resume", guardID},
				{"--ralph"},
			} {
				g := newCLIFixture()
				Expect(g.run(args...)).To(Equal(0), "%v: %s", args, g.errw.String())
				Expect(labelsOf(g.launched().Args)).NotTo(HaveKey("claude-sandbox.resume"), "%v", args)
			}
		})
	})

	Describe("CS-SESS-065: refusal", func() {
		It("CS-SESS-065: a sandbox labelled with the id and no record yet refuses with exit 4, creating nothing", func() {
			reg := filepath.Join(f.home, "registry")
			f.fake.On("docker ps", psRowResume("cs-proj-otter", f.proj, "claude", "otter", "7", "running", time.Now(), reg, guardID)+"\n", nil)
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
			Expect(creates(f)).To(BeEmpty())
			Expect(f.fake.Session).To(BeNil())
			msg := f.errw.String()
			Expect(msg).To(ContainSubstring("Error: conversation " + guardID + " is already open in 'otter' (cs-proj-otter);"))
			Expect(msg).To(ContainSubstring("a second session on it would interleave writes into one transcript."))
			Expect(msg).To(ContainSubstring("Attach to it:  cd " + f.proj + " && claude-sandbox --attach=otter"))
			Expect(msg).To(ContainSubstring("Or fork it:    add --fork-session after -- (a new conversation id)"))
		})

		It("CS-SESS-065: a ralph or headless holder is named without an attach line", func() {
			for _, mode := range []string{"ralph", "headless"} {
				g := newCLIFixture()
				reg := filepath.Join(g.home, "registry")
				writeFile(filepath.Join(reg, "7.json"), fmt.Sprintf(`{"pid":7,"sessionId":%q,"startedAt":%d}`, guardID, time.Now().Add(time.Hour).UnixMilli()))
				g.fake.On("docker ps", psRowResume("cs-proj-x", g.proj, mode, "", "7", "running", time.Now(), reg, "")+"\n", nil)
				Expect(g.run("--new", "--", "--resume", guardID)).To(Equal(4), mode)
				Expect(g.errw.String()).To(ContainSubstring("is already open in cs-proj-x;"), mode)
				Expect(g.errw.String()).NotTo(ContainSubstring("Attach to it"), mode)
			}
		})

		It("CS-SESS-065: --fork-session is the way out: the same launch with it goes ahead", func() {
			reg := filepath.Join(f.home, "registry")
			f.fake.On("docker ps", psRowResume("cs-proj-otter", f.proj, "claude", "otter", "7", "running", time.Now(), reg, guardID)+"\n", nil)
			Expect(f.run("--new", "--", "--resume", guardID, "--fork-session")).To(Equal(0), f.errw.String())
			Expect(creates(f)).To(HaveLen(1))
		})
	})

	It("CS-SESS-066: a running sandbox's record at its class, in the dir its registry label names, holds the id", func() {
		reg := filepath.Join(f.home, ".cache", "claude-sandbox", "peers", "sessions")
		writeFile(filepath.Join(reg, "263.json"), fmt.Sprintf(`{"pid":263,"sessionId":%q,"startedAt":%d}`, guardID, time.Now().Add(time.Hour).UnixMilli()))
		f.fake.On("docker ps", psRowResume("cs-other-heron", "/elsewhere", "claude", "heron", "7", "running", time.Now(), reg, "")+"\n", nil)
		Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
		Expect(f.errw.String()).To(ContainSubstring("Attach to it:  cd /elsewhere && claude-sandbox --attach=heron"))
	})

	It("CS-SESS-089: a stale record (its process gone) no longer blocks the resume; a failed docker top refuses naming it", func() {
		reg := filepath.Join(f.home, "registry")
		// A join that was OOM-killed with the id open: record at class+256.
		writeFile(filepath.Join(reg, "263.json"), fmt.Sprintf(`{"pid":263,"sessionId":%q,"startedAt":%d,"procStart":"555"}`, guardID, time.Now().Add(time.Hour).UnixMilli()))
		proc := filepath.Join(f.home, "proc")
		Expect(os.MkdirAll(filepath.Join(proc, "self", "ns"), 0o755)).To(Succeed())
		Expect(os.Symlink("pid:[42]", filepath.Join(proc, "self", "ns", "pid"))).To(Succeed())
		writeFile(filepath.Join(proc, "9001", "stat"), "9001 (claude) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 1\n")
		f.env.ProcRoot = proc
		row := psRowResume("cs-other-heron", "/elsewhere", "claude", "heron", "7", "running", time.Now(), reg, "") + "\n"
		f.fake.On("docker ps", row, nil)
		f.fake.On("docker top cs-other-heron -o pid", "PID\n9001\n", nil)
		Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(0), f.errw.String())
		Expect(creates(f)).To(HaveLen(1))

		g := newCLIFixture()
		writeFile(filepath.Join(reg, "263.json"), fmt.Sprintf(`{"pid":263,"sessionId":%q,"startedAt":%d,"procStart":"555"}`, guardID, time.Now().Add(time.Hour).UnixMilli()))
		g.env.ProcRoot = proc
		g.fake.On("docker ps", row, nil)
		g.fake.On("docker top", "", execx.Fail(1))
		Expect(g.run("--new", "--", "--resume", guardID)).To(Equal(4))
		Expect(g.errw.String()).To(ContainSubstring("(docker top cs-other-heron: "))
		Expect(creates(g)).To(BeEmpty())
	})

	Describe("CS-SESS-067: failing closed", func() {
		It("CS-SESS-067: a failed discovery refuses a resume with exit 4, and still launches a plain session", func() {
			// Discovery fails under the lock (earlier listings, before any
			// image work, are the session decision's own).
			f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
				if len(f.lock.acquiredAt) > 0 {
					return "", execx.Fail(1)
				}
				return "", nil
			})
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
			Expect(creates(f)).To(BeEmpty())
			Expect(f.errw.String()).To(ContainSubstring("Error: cannot tell whether conversation " + guardID + " is already open elsewhere"))
			Expect(f.errw.String()).To(ContainSubstring("so it is not resumed a second time."))
			Expect(f.errw.String()).To(ContainSubstring("Fix that and retry, or fork it: add --fork-session after --."))

			g := newCLIFixture()
			g.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
				if len(g.lock.acquiredAt) > 0 {
					return "", execx.Fail(1)
				}
				return "", nil
			})
			Expect(g.run("--new")).To(Equal(0), g.errw.String())
		})

		It("CS-SESS-067: an unreadable registry dir of a running sandbox refuses with exit 4", func() {
			target := filepath.Join(f.home, "real")
			Expect(os.MkdirAll(target, 0o755)).To(Succeed())
			link := filepath.Join(f.home, "linked-registry")
			Expect(os.Symlink(target, link)).To(Succeed())
			f.fake.On("docker ps", psRowResume("cs-a", "/elsewhere", "claude", "heron", "7", "running", time.Now(), link, "")+"\n", nil)
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
			Expect(f.errw.String()).To(ContainSubstring("the registry of cs-a"))
		})
	})

	It("CS-SESS-068: a live claude on the host holding the id refuses with exit 4", func() {
		proc := filepath.Join(f.home, "proc")
		Expect(os.MkdirAll(filepath.Join(proc, "self", "ns"), 0o755)).To(Succeed())
		Expect(os.Symlink("pid:[42]", filepath.Join(proc, "self", "ns", "pid"))).To(Succeed())
		writeFile(filepath.Join(proc, "4242", "stat"), "4242 (claude) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 777 1\n")
		writeFile(filepath.Join(f.home, ".claude", "sessions", "4242.json"),
			fmt.Sprintf(`{"pid":4242,"sessionId":%q,"startedAt":1,"procStart":"777","pidDomain":"linux::pid:[42]"}`, guardID))
		f.env.ProcRoot = proc
		Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
		Expect(creates(f)).To(BeEmpty())
		Expect(f.errw.String()).To(ContainSubstring("is already open in a claude process on this host (pid 4242);"))
		Expect(f.errw.String()).To(ContainSubstring("Or fork it:    add --fork-session after --"))
	})

	Describe("CS-SESS-069: under the launch lock", func() {
		It("CS-SESS-069: the check runs inside the lock, after discovery and before the create", func() {
			reg := filepath.Join(f.home, "registry")
			f.fake.On("docker ps", psRowResume("cs-proj-otter", f.proj, "claude", "otter", "7", "created", time.Now(), reg, guardID)+"\n", nil)
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
			held := f.lock.held()
			Expect(held).NotTo(BeEmpty())
			Expect(held[0]).To(HavePrefix("docker ps -a "))
			for _, l := range held {
				Expect(l).NotTo(HavePrefix("docker create"))
			}
		})

		It("CS-SESS-069: a racing launch's reservation seen on a create retry refuses the second attempt", func() {
			attempts := conflictOn(f, 1)
			reg := filepath.Join(f.home, "registry")
			f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) {
				if *attempts == 0 {
					return "", nil
				}
				return psRowResume("cs-proj-otter", f.proj, "claude", "otter", "7", "created", time.Now(), reg, guardID) + "\n", nil
			})
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(4))
			Expect(creates(f)).To(HaveLen(1), "one attempt, then the guard on the retry")
		})

		It("CS-SESS-069: a resume never launches without the lock; a plain launch still does", func() {
			f.lock.err = errors.New("timed out after 30s")
			Expect(f.run("--new", "--", "--resume", guardID)).To(Equal(2))
			Expect(f.errw.String()).To(ContainSubstring("Error: could not take the launch lock (timed out after 30s); not resuming " + guardID + " unserialized."))
			Expect(creates(f)).To(BeEmpty())

			g := newCLIFixture()
			g.lock.err = errors.New("timed out after 30s")
			Expect(g.run("--new")).To(Equal(0), g.errw.String())
			Expect(g.errw.String()).To(ContainSubstring("could not take the launch lock"))
			Expect(creates(g)).To(HaveLen(1))
		})
	})
})
