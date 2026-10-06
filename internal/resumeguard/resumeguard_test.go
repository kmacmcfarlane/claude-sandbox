package resumeguard_test

// Spec: spec/sessions.feature CS-SESS-065..068 — the resume guard's decision:
// which sandbox holds a conversation (rules a and b), the hardened registry
// reads, failing closed, and the host claude check. The launch-lock wiring,
// exit codes and messages are covered in
// cmd/claude-sandbox/resume_guard_cli_test.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

const (
	convID  = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
	otherID = "53cd0872-ec39-41a3-86bd-b000abb5fb32"
	// hostNS is the fixture's /proc/self/ns/pid link text.
	hostNS = "pid:[4026534579]"
)

var created = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// record is a registry record shaped like one Claude Code 2.1.284 wrote
// (fields trimmed to the ones that matter, plus a few that do not).
func record(pid int, id string, startedAt time.Time, domain, procStart string) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/p","startedAt":%d,"procStart":%q,"version":"2.1.284",`+
		`"kind":"interactive","pidDomain":%q,"name":"n","nameSource":"user","status":"idle"}`,
		pid, id, startedAt.UnixMilli(), procStart, domain)
}

type fixture struct {
	home, config, proc string
	sleeps             int
	fake               *execx.Fake
}

// livePID is the host pid the fixture's "docker top" lists; its stat's
// starttime is "1", the procStart record() fixtures carry by default.
const livePID = 9001

func newFixture() *fixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &fixture{home: filepath.Join(base, "home"), proc: filepath.Join(base, "proc")}
	f.config = filepath.Join(f.home, ".claude")
	Expect(os.MkdirAll(filepath.Join(f.proc, "self", "ns"), 0o755)).To(Succeed())
	Expect(os.Symlink(hostNS, filepath.Join(f.proc, "self", "ns", "pid"))).To(Succeed())
	f.fake = &execx.Fake{}
	f.fake.On("docker top", fmt.Sprintf("PID\n%d\n", livePID), nil)
	f.procStat(livePID, "1")
	return f
}

// tops counts the docker top calls.
func (f *fixture) tops() int {
	n := 0
	for _, l := range f.fake.CommandLines() {
		if strings.HasPrefix(l, "docker top ") {
			n++
		}
	}
	return n
}

func (f *fixture) write(dir string, pid int, body string) {
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o644)).To(Succeed())
}

// proc makes /proc/<pid>/stat with the given starttime (field 22) and a comm
// holding spaces and parentheses.
func (f *fixture) procStat(pid int, start string) {
	dir := filepath.Join(f.proc, fmt.Sprint(pid))
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	stat := fmt.Sprintf("%d (cl) aude (x)) S 6 318 1 34816 318 4194304 5285340 76737792 328 11320 59454 4057 125059 42346 20 0 37 0 %s 5816676352 119626\n", pid, start)
	Expect(os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)).To(Succeed())
}

func (f *fixture) check(ss ...sessions.Session) resumeguard.Check {
	return resumeguard.Check{
		ID: convID, Sessions: ss, Home: f.home, ConfigDir: f.config,
		ProcRoot: f.proc, GOOS: "linux", Runner: f.fake,
		MachineIDPath: filepath.Join(f.home, "no-machine-id"),
		Sleep:         func(time.Duration) { f.sleeps++ },
	}
}

func sandbox(name, state, class, registry, resume string) sessions.Session {
	return sessions.Session{
		Name: name, Project: "/proj", Mode: "claude", Instance: strings.TrimPrefix(name, "cs-"),
		State: state, PIDClass: class, RegistryDir: registry, Resume: resume, CreatedAt: created,
	}
}

var _ = Describe("resume guard", func() {
	var f *fixture
	var reg string
	BeforeEach(func() {
		f = newFixture()
		reg = filepath.Join(f.home, ".cache", "claude-sandbox", "peers", "sessions")
	})
	after := created.Add(time.Minute)

	Describe("CS-SESS-065: which sandbox holds the conversation", func() {
		It("CS-SESS-065 rule a: a labelled reservation, or a labelled running container with no record of its own yet", func() {
			for _, st := range []string{"created", "running", "paused"} {
				v := f.check(sandbox("cs-otter", st, "7", reg, convID)).Run()
				Expect(v.Open).To(BeTrue(), st)
				Expect(v.Holder).NotTo(BeNil(), st)
				Expect(v.Holder.Name).To(Equal("cs-otter"))
			}
			// The label is compared case-insensitively.
			v := f.check(sandbox("cs-otter", "running", "7", reg, strings.ToUpper(convID))).Run()
			Expect(v.Holder).NotTo(BeNil())
		})

		It("CS-SESS-065 rule a: a labelled container whose record names another id switched away; one naming the id still holds it", func() {
			f.write(reg, 7, record(7, otherID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-otter", "running", "7", reg, convID)).Run().Open).To(BeFalse())

			f.write(reg, 7+256, record(7+256, convID, after, "linux::pid:[1]", "1")) // a join onto it
			Expect(f.check(sandbox("cs-otter", "running", "7", reg, convID)).Run().Holder).NotTo(BeNil())
		})

		It("CS-SESS-065: a record older than the container is an earlier container's leftover", func() {
			f.write(reg, 7, record(7, otherID, created.Add(-time.Hour), "linux::pid:[1]", "1"))
			// Labelled, and the only record predates it: still opening the id.
			Expect(f.check(sandbox("cs-otter", "running", "7", reg, convID)).Run().Holder).NotTo(BeNil())

			f.write(reg, 7, record(7, convID, created.Add(-time.Hour), "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-otter", "running", "7", reg, "")).Run().Open).To(BeFalse(), "unlabelled, stale record")
		})

		It("CS-SESS-065 rule b: a running or paused container's record at its class names the id", func() {
			f.write(reg, 7+512, record(7+512, convID, after, "linux::pid:[1]", "1"))
			for _, st := range []string{"running", "paused", ""} {
				v := f.check(sandbox("cs-heron", st, "7", reg, "")).Run()
				Expect(v.Holder).NotTo(BeNil(), "state %q", st)
			}
			// Another class, or another id, holds nothing.
			Expect(f.check(sandbox("cs-heron", "running", "8", reg, "")).Run().Open).To(BeFalse())
			f.write(reg, 7+512, record(7+512, otherID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-heron", "running", "7", reg, "")).Run().Open).To(BeFalse())
		})

		It("CS-SESS-065: a row in any other state (exited) holds nothing, label or record", func() {
			f.write(reg, 7, record(7, convID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-gone", "exited", "7", reg, convID)).Run().Open).To(BeFalse())
		})
	})

	Describe("CS-SESS-066: hardened reads from the directory each container names", func() {
		It("CS-SESS-066: the registry label is read; a container without it is looked up in both fallback dirs", func() {
			f.write(filepath.Join(f.config, "sessions"), 9, record(9, convID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-old", "running", "9", "", "")).Run().Holder).NotTo(BeNil(), "<config dir>/sessions")
			Expect(f.check(sandbox("cs-new", "running", "9", reg, "")).Run().Open).To(BeFalse(), "the label names another dir")

			Expect(os.RemoveAll(filepath.Join(f.config, "sessions"))).To(Succeed())
			f.write(reg, 9, record(9, convID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-old", "running", "9", "", "")).Run().Holder).NotTo(BeNil(), "peers/sessions")
		})

		It("CS-SESS-066, CS-DIR-011: containers on the legacy and on the new peers root are each read from their label", func() {
			newReg := filepath.Join(f.home, ".local", "state", "claude-sandbox-peers", "sessions")
			f.write(newReg, 9, record(9, convID, after, "linux::pid:[1]", "1"))
			f.write(reg, 10, record(10, otherID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-new", "running", "9", newReg, "")).Run().Holder).NotTo(BeNil(), "new root")
			Expect(f.check(sandbox("cs-legacy", "running", "10", reg, "")).Run().Open).To(BeFalse())
			f.write(reg, 10, record(10, convID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-legacy", "running", "10", reg, "")).Run().Holder).NotTo(BeNil(), "legacy root")
			// A container without the label predates the move: the new root
			// is not a fallback.
			Expect(os.RemoveAll(reg)).To(Succeed())
			Expect(f.check(sandbox("cs-old", "running", "9", "", "")).Run().Open).To(BeFalse())
		})

		It("CS-SESS-066: a missing registry dir holds no records; a container without a class is judged by its label only", func() {
			Expect(f.check(sandbox("cs-a", "running", "7", filepath.Join(f.home, "nope"), "")).Run().Open).To(BeFalse())
			f.write(reg, 7, record(7, convID, after, "linux::pid:[1]", "1"))
			Expect(f.check(sandbox("cs-a", "running", "", reg, "")).Run().Open).To(BeFalse())
			Expect(f.check(sandbox("cs-a", "running", "", reg, convID)).Run().Holder).NotTo(BeNil())
		})

		It("CS-SESS-066: only the watched class is read, so a bad record elsewhere is not looked at", func() {
			f.write(reg, 8, "not json")
			Expect(os.WriteFile(filepath.Join(reg, "notes.txt"), []byte("x"), 0o644)).To(Succeed())
			Expect(f.check(sandbox("cs-a", "running", "7", reg, "")).Run().Open).To(BeFalse())
			Expect(f.sleeps).To(BeZero())
		})
	})

	Describe("CS-SESS-067: failing closed", func() {
		It("CS-SESS-067: a failed discovery counts as open", func() {
			c := f.check()
			c.DiscoveryErr = errors.New("listing sandbox containers: exit status 1")
			v := c.Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.Holder).To(BeNil())
			Expect(v.Reason).To(ContainSubstring("listing sandbox containers"))
		})

		It("CS-SESS-067: a registry dir that is a symlink or not a directory counts as open", func() {
			real := filepath.Join(f.home, "real")
			Expect(os.MkdirAll(real, 0o755)).To(Succeed())
			link := filepath.Join(f.home, "link")
			Expect(os.Symlink(real, link)).To(Succeed())
			v := f.check(sandbox("cs-a", "running", "7", link, "")).Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.Reason).To(ContainSubstring("cs-a"))
			Expect(v.Reason).To(ContainSubstring(link))

			file := filepath.Join(f.home, "file")
			Expect(os.WriteFile(file, nil, 0o644)).To(Succeed())
			Expect(f.check(sandbox("cs-a", "running", "7", file, "")).Run().Reason).NotTo(BeEmpty())
		})

		It("CS-SESS-067: a malformed record at a watched class counts as open: retried 3 times only when a partial write could explain it", func() {
			big := `{"pid":7,"sessionId":"` + otherID + `","pad":"` + strings.Repeat("x", registry.MaxRecordSize) + `"}`
			partial := map[string]bool{"unparsable": true, "oversized": true}
			cases := map[string]func(){
				"unparsable":        func() { f.write(reg, 7, "{") },
				"pid mismatch":      func() { f.write(reg, 7, record(8, otherID, after, "d", "1")) },
				"sessionId no uuid": func() { f.write(reg, 7, record(7, "my-name", after, "d", "1")) },
				"oversized":         func() { f.write(reg, 7, big) },
				"a symlink": func() {
					f.write(reg, 99, record(7, otherID, after, "d", "1"))
					Expect(os.Symlink("99.json", filepath.Join(reg, "7.json"))).To(Succeed())
				},
				"a FIFO": func() {
					Expect(os.MkdirAll(reg, 0o755)).To(Succeed())
					Expect(syscall.Mkfifo(filepath.Join(reg, "7.json"), 0o644)).To(Succeed())
				},
			}
			for name, plant := range cases {
				Expect(os.RemoveAll(reg)).To(Succeed())
				f.sleeps = 0
				plant()
				v := f.check(sandbox("cs-a", "running", "7", reg, "")).Run()
				Expect(v.Open).To(BeTrue(), name)
				Expect(v.Reason).To(ContainSubstring("7.json"), name)
				if partial[name] {
					Expect(f.sleeps).To(Equal(resumeguard.Retries), name)
				} else {
					Expect(f.sleeps).To(BeZero(), "%s fails at once", name)
				}
			}
		})

		It("CS-SESS-067: a registry dir holding more than MaxEntries entries counts as open", func() {
			Expect(os.MkdirAll(reg, 0o755)).To(Succeed())
			for i := 0; i <= registry.MaxEntries; i++ {
				Expect(os.WriteFile(filepath.Join(reg, fmt.Sprintf("x%d", i)), nil, 0o644)).To(Succeed())
			}
			v := f.check(sandbox("cs-a", "running", "7", reg, "")).Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.Reason).To(ContainSubstring("more than"))
		})

		It("CS-SESS-067: a holder found elsewhere is named rather than the unreadable one", func() {
			f.write(reg, 5, record(5, convID, after, "d", "1"))
			notDir := filepath.Join(f.home, "file-not-dir")
			Expect(os.WriteFile(notDir, nil, 0o644)).To(Succeed())
			bad := f.check(sandbox("cs-bad", "running", "7", notDir, "")).Run()
			Expect(bad.Reason).To(ContainSubstring("cs-bad"), "unreadable on its own")
			v := f.check(
				sandbox("cs-bad", "running", "7", notDir, ""),
				sandbox("cs-holder", "running", "5", reg, ""),
			).Run()
			Expect(v.Holder).NotTo(BeNil())
			Expect(v.Holder.Name).To(Equal("cs-holder"))
		})
	})

	Describe("CS-SESS-089: a matching record is confirmed alive", func() {
		It("CS-SESS-089: one docker top on a match, and none when nothing matches", func() {
			f.write(reg, 7, record(7, otherID, after, "d", "1"))
			f.write(reg, 8, record(8, convID, after, "d", "1"))
			Expect(f.check(sandbox("cs-a", "running", "7", reg, ""), sandbox("cs-b", "running", "9", reg, "")).Run().Open).To(BeFalse())
			Expect(f.tops()).To(BeZero())

			v := f.check(sandbox("cs-a", "running", "7", reg, ""), sandbox("cs-b", "running", "8", reg, "")).Run()
			Expect(v.Holder).NotTo(BeNil())
			Expect(v.Holder.Name).To(Equal("cs-b"))
			Expect(f.fake.CommandLines()).To(Equal([]string{"docker top cs-b -o pid"}))
		})

		It("CS-SESS-089: a record whose process is gone holds nothing; a labelled container with no other record still holds by its label", func() {
			f.write(reg, 7, record(7, convID, after, "d", "4242")) // no listed pid started at 4242
			Expect(f.check(sandbox("cs-a", "running", "7", reg, "")).Run().Open).To(BeFalse())
			Expect(f.check(sandbox("cs-a", "running", "7", reg, convID)).Run().Holder).NotTo(BeNil())
			f.write(reg, 7+256, record(7+256, otherID, after, "d", "1"))
			Expect(f.check(sandbox("cs-a", "running", "7", reg, convID)).Run().Open).To(BeFalse(), "switched away; the join on the id died")
		})

		It("CS-SESS-089: a listed pid whose stat cannot be read, or a record without procStart, cannot be ruled out", func() {
			f.write(reg, 7, record(7, convID, after, "d", "4242"))
			Expect(os.RemoveAll(filepath.Join(f.proc, fmt.Sprint(livePID), "stat"))).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(f.proc, fmt.Sprint(livePID), "stat"), 0o755)).To(Succeed())
			Expect(f.check(sandbox("cs-a", "running", "7", reg, "")).Run().Holder).NotTo(BeNil())

			g := newFixture()
			greg := filepath.Join(g.home, "reg")
			g.write(greg, 7, fmt.Sprintf(`{"pid":7,"sessionId":%q,"startedAt":%d}`, convID, after.UnixMilli()))
			Expect(g.check(sandbox("cs-a", "running", "7", greg, "")).Run().Holder).NotTo(BeNil())
			Expect(g.tops()).To(BeZero())
		})

		It("CS-SESS-089: no listed pid visible, or nothing listed, cannot rule the record out", func() {
			f.write(reg, 7, record(7, convID, after, "d", "4242"))
			// A nested launcher: docker top lists host pids its /proc lacks.
			g := newFixture()
			g.fake = &execx.Fake{}
			g.fake.On("docker top", "PID\n7777\n7778\n", nil)
			v := g.check(sandbox("cs-a", "running", "7", reg, "")).Run()
			Expect(v.Holder).NotTo(BeNil(), "no listed pid visible")

			h := newFixture()
			h.fake = &execx.Fake{}
			h.fake.On("docker top", "PID\n", nil)
			Expect(h.check(sandbox("cs-a", "running", "7", reg, "")).Run().Holder).NotTo(BeNil(), "nothing listed")

			// One visible pid with another start time does rule it out.
			Expect(f.check(sandbox("cs-a", "running", "7", reg, "")).Run().Open).To(BeFalse())
		})

		It("CS-SESS-089: a hung docker top is given up after the bound plus at most about 500 ms", func() {
			saved := resumeguard.TopTimeout
			resumeguard.TopTimeout = 50 * time.Millisecond
			DeferCleanup(func() { resumeguard.TopTimeout = saved })
			f.write(reg, 7, record(7, convID, after, "d", "1"))
			hung := &hangingRunner{Fake: f.fake, release: make(chan struct{})}
			DeferCleanup(func() { close(hung.release) })
			c := f.check(sandbox("cs-a", "running", "7", reg, ""))
			c.Runner = hung
			start := time.Now()
			v := c.Run()
			Expect(time.Since(start)).To(BeNumerically("<", 900*time.Millisecond))
			Expect(v.Reason).To(ContainSubstring("docker top cs-a"))
			Expect(hung.killedGroup).To(BeTrue(), "the whole process group is killed")
		})

		It("CS-SESS-089: a failed docker top fails closed, naming it", func() {
			g := newFixture()
			g.fake = &execx.Fake{}
			g.fake.On("docker top", "", execx.Fail(1))
			greg := filepath.Join(g.home, "reg")
			g.write(greg, 7, record(7, convID, after, "d", "1"))
			v := g.check(sandbox("cs-a", "running", "7", greg, "")).Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.Holder).To(BeNil())
			Expect(v.Reason).To(ContainSubstring("docker top cs-a"))
		})

		It("CS-SESS-065: a record in the launcher's own pid namespace belongs to host claude, not to the container", func() {
			host := filepath.Join(f.config, "sessions")
			f.write(host, 7+256, record(7+256, convID, after, "linux::"+hostNS, "777"))
			// Not live on the host (no /proc/263): nothing holds it.
			Expect(f.check(sandbox("cs-a", "running", "7", host, "")).Run().Open).To(BeFalse())
			Expect(f.tops()).To(BeZero())
		})
	})

	It("CS-SESS-066: under go test the real home and the real ~/.claude are never read", func() {
		h, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		c := f.check()
		c.Home = h
		Expect(func() { c.Run() }).To(Panic())
		c = f.check()
		c.ConfigDir = filepath.Join(h, ".claude")
		Expect(func() { c.Run() }).To(Panic())
	})

	Describe("CS-SESS-068: a claude running on the host", func() {
		host := "linux::" + hostNS
		var dir string
		BeforeEach(func() { dir = filepath.Join(f.home, ".claude", "sessions") })

		It("CS-SESS-068: the machine-id is /etc/machine-id trimmed, empty when absent, and part of the host domain", func() {
			f.procStat(263, "777")
			// No machine-id (a container-like host): linux::<ns> matches.
			f.write(dir, 7+256, record(7+256, convID, after, host, "777"))
			Expect(f.check().Run().Open).To(BeTrue())
			Expect(os.Remove(filepath.Join(dir, fmt.Sprint(7+256)+".json"))).To(Succeed())

			// With one (trailing newline): only linux:<mid>:<ns> matches.
			mid := filepath.Join(f.home, "machine-id")
			Expect(os.WriteFile(mid, []byte("0123abcd\n"), 0o644)).To(Succeed())
			c := f.check()
			c.MachineIDPath = mid
			f.write(dir, 7+256, record(7+256, convID, after, host, "777"))
			Expect(c.Run().Open).To(BeFalse(), "an empty-id record is not this host's")
			f.write(dir, 7+256, record(7+256, convID, after, "linux:0123abcd:"+hostNS, "777"))
			Expect(c.Run().Open).To(BeTrue())
			f.write(dir, 7+256, record(7+256, convID, after, "linux:0123abcd\n:"+hostNS, "777"))
			Expect(c.Run().Open).To(BeFalse())
		})

		It("CS-SESS-068: procStart is field 22 of /proc/<pid>/stat, split after the last \")\"", func() {
			// A line from a live 2.1.284 session whose record said procStart "50424".
			live := "318 (claude) S 6 318 1 34816 318 4194304 5285340 76737792 328 11320 59454 4057 125059 42346 20 0 37 0 50424 5816676352 119626 18446744073709551615"
			start, ok := resumeguard.StartTime(live)
			Expect(ok).To(BeTrue())
			Expect(start).To(Equal("50424"))
			start, ok = resumeguard.StartTime("7 (a) b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 99 1")
			Expect(ok).To(BeTrue())
			Expect(start).To(Equal("99"))
			_, ok = resumeguard.StartTime("7 (claude S 1 2")
			Expect(ok).To(BeFalse())
		})

		It("CS-SESS-068: a live host record in the host's namespace holds the id", func() {
			f.write(dir, 4242, record(4242, convID, after, host, "12345"))
			f.procStat(4242, "12345")
			v := f.check().Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.HostPID).To(Equal(4242))
		})

		It("CS-SESS-068: the config dir's registry is read too", func() {
			cfg := filepath.Join(f.home, "alt-config")
			f.write(filepath.Join(cfg, "sessions"), 4242, record(4242, convID, after, host, "12345"))
			f.procStat(4242, "12345")
			c := f.check()
			c.ConfigDir = cfg
			Expect(c.Run().HostPID).To(Equal(4242))
		})

		It("CS-SESS-068: absent, reused, another namespace or another id is not live", func() {
			f.write(dir, 4242, record(4242, convID, after, host, "12345"))
			Expect(f.check().Run().Open).To(BeFalse(), "no /proc/4242")
			f.procStat(4242, "99999")
			Expect(f.check().Run().Open).To(BeFalse(), "pid reused")
			f.procStat(4242, "12345")
			f.write(dir, 4242, record(4242, convID, after, "linux::pid:[1]", "12345"))
			Expect(f.check().Run().Open).To(BeFalse(), "a sandbox's namespace")
			f.write(dir, 4242, record(4242, otherID, after, host, "12345"))
			Expect(f.check().Run().Open).To(BeFalse(), "another conversation")
		})

		It("CS-SESS-068: a /proc/<pid> whose stat cannot be read or parsed fails closed", func() {
			f.write(dir, 4242, record(4242, convID, after, host, "12345"))
			Expect(os.MkdirAll(filepath.Join(f.proc, "4242", "stat"), 0o755)).To(Succeed()) // EISDIR on read
			Expect(f.check().Run().HostPID).To(Equal(4242))
			Expect(os.RemoveAll(filepath.Join(f.proc, "4242", "stat"))).To(Succeed())
			Expect(os.WriteFile(filepath.Join(f.proc, "4242", "stat"), []byte("garbage"), 0o644)).To(Succeed())
			Expect(f.check().Run().HostPID).To(Equal(4242))
		})

		It("CS-SESS-068: a malformed host record is skipped; an unreadable namespace or directory fails closed", func() {
			f.write(dir, 4242, "{")
			Expect(f.check().Run().Open).To(BeFalse())

			Expect(os.Remove(filepath.Join(f.proc, "self", "ns", "pid"))).To(Succeed())
			v := f.check().Run()
			Expect(v.Open).To(BeTrue())
			Expect(v.Reason).To(ContainSubstring("pid namespace"))

			g := newFixture()
			Expect(os.MkdirAll(g.home, 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(g.home, "elsewhere"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(g.config, 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(g.home, "elsewhere"), filepath.Join(g.config, "sessions"))).To(Succeed())
			Expect(g.check().Run().Reason).To(ContainSubstring("sessions"))
		})

		It("CS-SESS-068: skipped on other systems", func() {
			f.write(dir, 4242, record(4242, convID, after, host, "12345"))
			f.procStat(4242, "12345")
			c := f.check()
			c.GOOS = "darwin"
			Expect(c.Run().Open).To(BeFalse())
		})
	})
})

// hangingRunner starts processes that never exit, even when killed, and
// records whether their group was killed.
type hangingRunner struct {
	*execx.Fake
	release     chan struct{}
	killedGroup bool
}

func (h *hangingRunner) Start(execx.Cmd) (execx.Process, error) {
	return &hangingProcess{r: h}, nil
}

type hangingProcess struct{ r *hangingRunner }

func (p *hangingProcess) Signal(os.Signal) error { return nil }
func (p *hangingProcess) Wait() error            { <-p.r.release; return nil }
func (p *hangingProcess) Pid() int               { return 1 }
func (p *hangingProcess) KillGroup() error       { p.r.killedGroup = true; return nil }
