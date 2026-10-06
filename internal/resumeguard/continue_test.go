package resumeguard_test

// Spec: spec/sessions.feature CS-SESS-093..095 — the continue guard's
// decision: rules a', b' and h', the target directories (exact and the
// worktree subtree, inTree), the config-dir rule, CS-SESS-089 liveness and
// failing closed. The wiring (labels, lock, messages, joins, the pre-build
// check) is covered in cmd/claude-sandbox/continue_guard_cli_test.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// recordIn is record() with a cwd.
func recordIn(pid int, id, cwd string, startedAt time.Time, domain, procStart string) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"procStart":%q,"version":"2.1.290",`+
		`"kind":"interactive","entrypoint":"cli","pidDomain":%q}`,
		pid, id, cwd, startedAt.UnixMilli(), procStart, domain)
}

var _ = Describe("continue guard", func() {
	var f *fixture
	var reg, proj string
	after := created.Add(time.Minute)
	BeforeEach(func() {
		f = newFixture()
		reg = filepath.Join(f.home, ".cache", "claude-sandbox", "peers", "sessions")
		proj = filepath.Join(f.home, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})
	cont := func(ss ...sessions.Session) resumeguard.Check {
		c := f.check(ss...)
		c.ID = ""
		c.Continue = &resumeguard.ContinueTarget{Exact: []string{proj}, Project: proj}
		return c
	}
	box := func(name, state, class, project string) sessions.Session {
		s := sandbox(name, state, class, reg, "")
		s.Project = project
		return s
	}

	Describe("CS-SESS-093: the target directories", func() {
		It("CS-SESS-093: InTree is the root or below it, by whole components; ..foo stays inside", func() {
			Expect(resumeguard.InTree("/wt/foo", "/wt/foo")).To(BeTrue())
			Expect(resumeguard.InTree("/wt/foo/a/b", "/wt/foo")).To(BeTrue())
			Expect(resumeguard.InTree("/wt/foo/..foo", "/wt/foo")).To(BeTrue(), `a component named "..foo" is inside`)
			Expect(resumeguard.InTree("/wt/foo", "/wt/fo")).To(BeFalse(), "a sibling prefix")
			Expect(resumeguard.InTree("/wt", "/wt/foo")).To(BeFalse())
			Expect(resumeguard.InTree("/wt/foo/../bar", "/wt/foo")).To(BeFalse())
			Expect(resumeguard.InTree("relative", "/wt/foo")).To(BeFalse(), "Rel fails: not inside")
			Expect(resumeguard.InTree("/nope/x", "/also/nope")).To(BeFalse(), "unresolvable both ways")
		})

		It("CS-SESS-093: InTree follows a symlinked root where both resolve", func() {
			real := filepath.Join(f.home, "real-wt")
			Expect(os.MkdirAll(filepath.Join(real, "sub"), 0o755)).To(Succeed())
			link := filepath.Join(f.home, "link-wt")
			Expect(os.Symlink(real, link)).To(Succeed())
			Expect(resumeguard.InTree(filepath.Join(real, "sub"), link)).To(BeTrue())
			Expect(resumeguard.InTree(filepath.Join(link, "sub"), real)).To(BeTrue())
		})

		It("CS-SESS-093: the exact entry matches that directory only; an Under entry its subtree", func() {
			t := resumeguard.ContinueTarget{Exact: []string{"/p"}, Under: []string{"/r/.claude/worktrees/otter"}}
			Expect(t.Has("/p")).To(BeTrue())
			Expect(t.Has("/p/")).To(BeTrue())
			Expect(t.Has("/p/sub")).To(BeFalse())
			Expect(t.Has("/r/.claude/worktrees/otter")).To(BeTrue())
			Expect(t.Has("/r/.claude/worktrees/otter/pkg")).To(BeTrue())
			Expect(t.Has("/r/.claude/worktrees/otters")).To(BeFalse())
			Expect(t.Has("")).To(BeFalse())
		})
	})

	Describe("CS-SESS-093 rule b': a live record whose cwd is in T", func() {
		It("CS-SESS-093: a record in the launch's directory holds, naming its conversation and cwd", func() {
			f.write(reg, 7+256, recordIn(7+256, otherID, proj, after, "linux::pid:[1]", "1"))
			v := cont(box("cs-email", "running", "7", "/elsewhere")).RunContinue()
			Expect(v.Open).To(BeTrue())
			Expect(v.Holder).NotTo(BeNil())
			Expect(v.Holder.Name).To(Equal("cs-email"))
			Expect(v.SessionID).To(Equal(otherID))
			Expect(v.Cwd).To(Equal(proj))
			Expect(v.Starting).To(BeFalse())
			Expect(f.tops()).To(Equal(1), "confirmed alive with one docker top (CS-SESS-089)")
		})

		It("CS-SESS-093: another cwd, a leftover from before creation, the launcher's own domain and another class hold nothing", func() {
			s := box("cs-a", "running", "7", "/elsewhere")
			f.write(reg, 7, recordIn(7, otherID, "/other/dir", after, "linux::pid:[1]", "1"))
			Expect(cont(s).RunContinue().Open).To(BeFalse(), "another cwd")
			f.write(reg, 7, recordIn(7, otherID, proj, created.Add(-time.Hour), "linux::pid:[1]", "1"))
			Expect(cont(s).RunContinue().Open).To(BeFalse(), "a leftover")
			f.write(reg, 7, recordIn(7, otherID, proj, after, "linux::"+hostNS, "1"))
			Expect(cont(s).RunContinue().Open).To(BeFalse(), "the host's own claude is h's, not b's")
			f.write(reg, 7, recordIn(7, otherID, proj, after, "linux::pid:[1]", "1"))
			Expect(cont(box("cs-a", "running", "8", "/elsewhere")).RunContinue().Open).To(BeFalse(), "another class")
			Expect(f.tops()).To(BeZero())
		})

		It("CS-SESS-093: a record below a named worktree holds; below the plain directory it does not", func() {
			wt := filepath.Join(proj, ".claude", "worktrees", "otter")
			f.write(reg, 7, recordIn(7, otherID, filepath.Join(wt, "pkg"), after, "linux::pid:[1]", "1"))
			s := box("cs-a", "running", "7", "/elsewhere")
			Expect(cont(s).RunContinue().Open).To(BeFalse())
			c := cont(s)
			c.Continue.Under = []string{wt}
			Expect(c.RunContinue().Holder).NotTo(BeNil())
		})

		It("CS-SESS-093, CS-SESS-089: a record whose process is gone is dropped; a failed docker top fails closed", func() {
			f.write(reg, 7, recordIn(7, otherID, proj, after, "linux::pid:[1]", "999"))
			Expect(cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue().Open).To(BeFalse())

			f.fake = &execx.Fake{}
			f.fake.On("docker top", "", execx.Fail(1))
			v := cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue()
			Expect(v.Open).To(BeTrue())
			Expect(v.Holder).To(BeNil())
			Expect(v.Reason).To(ContainSubstring("docker top cs-a"))
		})

		It("CS-SESS-093: a sandbox record at a watched class with no cwd fails closed; a host one is skipped", func() {
			f.write(reg, 7, fmt.Sprintf(`{"pid":7,"sessionId":%q,"startedAt":%d,"pidDomain":"linux::pid:[1]"}`, otherID, after.UnixMilli()))
			v := cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue()
			Expect(v.Open).To(BeTrue())
			Expect(v.Holder).To(BeNil())
			Expect(v.Reason).To(ContainSubstring("7.json: no cwd"))
			// A record in the launcher's own pid domain (host claude's, in an
			// unbridged sandbox's registry dir) is skipped before the cwd
			// check: rule h' judges it, and h' skips a cwd-less host record.
			f.write(reg, 7, fmt.Sprintf(`{"pid":7,"sessionId":%q,"startedAt":%d,"pidDomain":"linux::%s"}`, otherID, after.UnixMilli(), hostNS))
			own := cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue()
			Expect(own.Open).To(BeFalse(), "own-domain record without cwd: no refusal, no cannot-tell (%q)", own.Reason)
			// A leftover from before the container, or another class, is not watched.
			f.write(reg, 7, fmt.Sprintf(`{"pid":7,"sessionId":%q,"startedAt":%d}`, otherID, created.Add(-time.Hour).UnixMilli()))
			Expect(cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue().Open).To(BeFalse())

			f.procStat(4242, "77")
			f.write(filepath.Join(f.config, "sessions"), 4242, fmt.Sprintf(`{"pid":4242,"sessionId":%q,"startedAt":1,"procStart":"77","pidDomain":"linux::%s"}`, otherID, hostNS))
			Expect(cont().RunContinue().Open).To(BeFalse(), "CS-SESS-068: a host record without cwd is skipped")
		})

		It("CS-SESS-093: a record's kind and entrypoint are not consulted (F0)", func() {
			f.write(reg, 7, fmt.Sprintf(`{"pid":7,"sessionId":%q,"cwd":%q,"startedAt":%d,"kind":"bg","entrypoint":"sdk-ts","pidDomain":"linux::pid:[1]"}`,
				otherID, proj, after.UnixMilli()))
			Expect(cont(box("cs-paseo", "running", "7", "/elsewhere")).RunContinue().Holder).NotTo(BeNil())
		})
	})

	Describe("CS-SESS-093 rule a': a sandbox of the project still starting", func() {
		It("CS-SESS-093: a continue- or resume-labelled reservation or record-less container of the project holds", func() {
			for _, st := range []string{"created", "running", "paused"} {
				s := box("cs-otter", st, "7", proj)
				s.Continue = true
				v := cont(s).RunContinue()
				Expect(v.Holder).NotTo(BeNil(), st)
				Expect(v.Starting).To(BeTrue(), st)
			}
			s := box("cs-otter", "running", "7", proj)
			s.Resume = convID
			Expect(cont(s).RunContinue().Starting).To(BeTrue(), "a resume label too")
		})

		It("CS-SESS-093: an unlabelled one, another project's, or one whose record exists does not", func() {
			Expect(cont(box("cs-otter", "created", "7", proj)).RunContinue().Open).To(BeFalse(), "no label")
			other := box("cs-otter", "created", "7", "/elsewhere")
			other.Continue = true
			Expect(cont(other).RunContinue().Open).To(BeFalse(), "another project")

			s := box("cs-otter", "running", "7", proj)
			s.Continue = true
			f.write(reg, 7, recordIn(7, otherID, "/other/dir", after, "linux::pid:[1]", "1"))
			Expect(cont(s).RunContinue().Open).To(BeFalse(), "it is up, elsewhere")
		})

		It("CS-SESS-094: the pre-build check (RecordsOnly) never holds by label or reservation", func() {
			s := box("cs-otter", "created", "7", proj)
			s.Continue = true
			c := cont(s)
			c.RecordsOnly = true
			Expect(c.RunContinue().Open).To(BeFalse())
			s.State = "running"
			c = cont(s)
			c.RecordsOnly = true
			Expect(c.RunContinue().Open).To(BeFalse())
			// A live record still holds there.
			f.write(reg, 7, recordIn(7, otherID, proj, after, "linux::pid:[1]", "1"))
			Expect(c.RunContinue().Holder).NotTo(BeNil())
		})
	})

	Describe("CS-SESS-093: the holder's config dir", func() {
		write := func() { f.write(reg, 7, recordIn(7, otherID, proj, after, "linux::pid:[1]", "1")) }
		It("CS-SESS-093: no registry label = unknown, counts; an empty configdir label next to it means <home>/.claude", func() {
			write()
			s := box("cs-a", "running", "7", "/elsewhere")
			Expect(cont(s).RunContinue().Holder).NotTo(BeNil(), "configdir empty = <home>/.claude = the launch's")

			c := cont(s)
			c.ConfigDir = filepath.Join(f.home, "work-claude")
			Expect(os.MkdirAll(c.ConfigDir, 0o755)).To(Succeed())
			Expect(c.RunContinue().Holder).NotTo(BeNil(), "<home>/.claude does not exist: cannot be told apart, counts")
			Expect(os.MkdirAll(f.config, 0o755)).To(Succeed())
			Expect(c.RunContinue().Open).To(BeFalse(), "both resolve and differ: provably another store")

			old := s
			old.RegistryDir = ""
			// Registry fallback reads <config dir>/sessions and the legacy root.
			c = cont(old)
			c.ConfigDir = filepath.Join(f.home, "work-claude")
			Expect(c.RunContinue().Holder).NotTo(BeNil(), "a container without the labels counts")
		})

		It("CS-SESS-093: a configdir label equal after resolution counts; an unresolvable one counts", func() {
			write()
			Expect(os.MkdirAll(f.config, 0o755)).To(Succeed())
			link := filepath.Join(f.home, "claude-link")
			Expect(os.Symlink(f.config, link)).To(Succeed())
			s := box("cs-a", "running", "7", "/elsewhere")
			s.ConfigDirEnv = link
			Expect(cont(s).RunContinue().Holder).NotTo(BeNil(), "the same dir through a link")
			s.ConfigDirEnv = filepath.Join(f.home, "missing")
			Expect(cont(s).RunContinue().Holder).NotTo(BeNil(), "cannot resolve: counts")
		})
	})

	Describe("CS-SESS-094: a claude on the host, and failing closed", func() {
		hostRec := func(pid int, cwd, procStart string) {
			f.write(filepath.Join(f.config, "sessions"), pid, recordIn(pid, otherID, cwd, after, "linux::"+hostNS, procStart))
		}
		It("CS-SESS-094: a live host record in the launch's config dir with its cwd in T holds", func() {
			f.procStat(4242, "77")
			hostRec(4242, proj, "77")
			v := cont().RunContinue()
			Expect(v.HostPID).To(Equal(4242))
			Expect(v.SessionID).To(Equal(otherID))
			Expect(v.Cwd).To(Equal(proj))
		})

		It("CS-SESS-094: a dead, reused or elsewhere host record holds nothing; ~/.claude is not read for another config dir", func() {
			hostRec(4242, proj, "77") // /proc/4242 absent
			Expect(cont().RunContinue().Open).To(BeFalse())
			f.procStat(4242, "78")
			Expect(cont().RunContinue().Open).To(BeFalse(), "reused pid")
			f.procStat(4242, "77")
			hostRec(4242, "/other", "77")
			Expect(cont().RunContinue().Open).To(BeFalse(), "another cwd")

			hostRec(4242, proj, "77")
			c := cont()
			c.ConfigDir = filepath.Join(f.home, "work-claude")
			Expect(c.RunContinue().Open).To(BeFalse(), "only <launch config dir>/sessions is read")
		})

		It("CS-SESS-094: discovery failure, an unreadable registry and an unreadable ns link fail closed", func() {
			c := cont()
			c.DiscoveryErr = errors.New("listing sandbox containers: exit status 1")
			Expect(c.RunContinue().Reason).To(ContainSubstring("listing sandbox containers"))

			notDir := filepath.Join(f.home, "file-not-dir")
			Expect(os.WriteFile(notDir, nil, 0o644)).To(Succeed())
			s := box("cs-bad", "running", "7", "/elsewhere")
			s.RegistryDir = notDir
			v := cont(s).RunContinue()
			Expect(v.Open).To(BeTrue())
			Expect(v.Reason).To(ContainSubstring("the registry of cs-bad"))

			f.write(reg, 7, "{")
			v = cont(box("cs-a", "running", "7", "/elsewhere")).RunContinue()
			Expect(v.Reason).To(ContainSubstring("7.json"))
			Expect(f.sleeps).To(Equal(resumeguard.Retries), "a partial write is retried")

			Expect(os.Remove(filepath.Join(f.proc, "self", "ns", "pid"))).To(Succeed())
			Expect(cont().RunContinue().Reason).To(ContainSubstring("pid namespace"))
		})
	})
})
