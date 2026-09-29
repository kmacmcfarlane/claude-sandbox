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
}

func newFixture() *fixture {
	base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	f := &fixture{home: filepath.Join(base, "home"), proc: filepath.Join(base, "proc")}
	f.config = filepath.Join(f.home, ".claude")
	Expect(os.MkdirAll(filepath.Join(f.proc, "self", "ns"), 0o755)).To(Succeed())
	Expect(os.Symlink(hostNS, filepath.Join(f.proc, "self", "ns", "pid"))).To(Succeed())
	return f
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
		ProcRoot: f.proc, GOOS: "linux",
		Sleep: func(time.Duration) { f.sleeps++ },
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

		It("CS-SESS-065: an exited kept container holds nothing, label or record", func() {
			f.write(reg, 7, record(7, convID, after, "linux::pid:[1]", "1"))
			s := sandbox("cs-kept", "exited", "7", reg, convID)
			s.Keep = "unless-stopped"
			Expect(f.check(s).Run().Open).To(BeFalse())
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

		It("CS-SESS-067: a malformed record at a watched class is retried 3 times, then counts as open", func() {
			big := `{"pid":7,"sessionId":"` + otherID + `","pad":"` + strings.Repeat("x", resumeguard.MaxRecordSize) + `"}`
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
				Expect(f.sleeps).To(Equal(resumeguard.Retries), name)
			}
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

	Describe("CS-SESS-068: a claude running on the host", func() {
		host := "linux::" + hostNS
		var dir string
		BeforeEach(func() { dir = filepath.Join(f.home, ".claude", "sessions") })

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
