package resumeguard_test

// Spec: spec/sessions.feature CS-SESS-106 — the guard's readers the restart
// shares: RegistryDirs (the one builder of a container's registry dirs),
// ReadClass (the fail-closed class read, with its test guard) and AliveRecord
// (CS-SESS-089's liveness).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/registry"
	"github.com/kmacmcfarlane/claude-sandbox/internal/resumeguard"
)

var _ = Describe("the readers the restart shares (CS-SESS-106)", func() {
	DescribeTable("CS-SESS-106: RegistryDirs is the label's dir, else the config dir's and the legacy peers root's",
		func(label, configDir, home string, want []string) {
			Expect(resumeguard.RegistryDirs(label, configDir, home)).To(Equal(want))
		},
		Entry("the label wins", "/s/peers/sessions", "/h/.claude", "/h", []string{"/s/peers/sessions"}),
		Entry("no label: both fallbacks", "", "/h/.claude", "/h",
			[]string{"/h/.claude/sessions", "/h/.cache/claude-sandbox/peers/sessions"}),
		Entry("no label, no home", "", "/h/.claude", "", []string{"/h/.claude/sessions"}),
		Entry("no label, no config dir", "", "", "/h", []string{"/h/.cache/claude-sandbox/peers/sessions"}),
		Entry("nothing", "", "", "", nil),
	)

	Describe("ReadClass", func() {
		var (
			f      *fixture
			reg    string
			sleeps int
		)
		sleep := func(time.Duration) { sleeps++ }
		BeforeEach(func() {
			f = newFixture()
			reg = filepath.Join(f.home, "reg")
			sleeps = 0
		})

		It("CS-SESS-106: reads the records at the class across the dirs; a missing dir holds none", func() {
			f.write(reg, 263, record(263, convID, created, "", "1"))
			f.write(reg, 264, record(264, otherID, created, "", "1"))
			recs, err := resumeguard.ReadClass([]string{filepath.Join(f.home, "missing"), reg}, 7, sleep)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].PID).To(Equal(263))
			Expect(recs[0].SessionID).To(Equal(convID))
		})

		It("CS-SESS-106: a record that stays malformed is an error after the retries, never skipped", func() {
			f.write(reg, 263, `{"pid":263,`)
			f.write(reg, 519, record(519, otherID, created, "", "1")) // a valid join at the class
			_, err := resumeguard.ReadClass([]string{reg}, 7, sleep)
			Expect(err).To(MatchError(registry.ErrPartial))
			Expect(sleeps).To(Equal(resumeguard.Retries))
		})

		It("CS-SESS-106: a partial record that becomes whole within the retries is read", func() {
			f.write(reg, 263, `{"pid":263,`)
			fixed := func(time.Duration) {
				sleeps++
				if sleeps == 2 {
					f.write(reg, 263, record(263, convID, created, "", "1"))
				}
			}
			recs, err := resumeguard.ReadClass([]string{reg}, 7, fixed)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(sleeps).To(Equal(2))
		})

		It("CS-SESS-106: a structural failure is an error at once (no retry)", func() {
			f.write(reg, 263, record(264, convID, created, "", "1")) // pid mismatch
			_, err := resumeguard.ReadClass([]string{reg}, 7, sleep)
			Expect(err).To(MatchError(registry.ErrMalformed))
			Expect(sleeps).To(BeZero())
		})

		It("CS-SESS-106: under go test a dir under a real registry root panics before anything is opened", func() {
			home, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			Expect(func() {
				resumeguard.ReadClass([]string{filepath.Join(home, ".claude", "sessions")}, 7, sleep)
			}).To(PanicWith(ContainSubstring("real registry")))
			Expect(func() {
				resumeguard.ReadClass([]string{reg}, 7, sleep)
			}).NotTo(Panic())
		})
	})

	Describe("AliveRecord", func() {
		var f *fixture
		BeforeEach(func() { f = newFixture() })

		rec := func(pid int, procStart string) registry.Record {
			return registry.Record{PID: pid, SessionID: convID, ProcStart: procStart}
		}

		It("CS-SESS-106: a record without procStart is alive with no docker call", func() {
			got, ok, err := resumeguard.AliveRecord(f.fake, f.proc, "cs-a", []registry.Record{rec(263, "")})
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(got.PID).To(Equal(263))
			Expect(f.tops()).To(BeZero())
		})

		It("CS-SESS-106: a listed pid with the record's start time keeps it alive; another start rules it out", func() {
			_, ok, err := resumeguard.AliveRecord(f.fake, f.proc, "cs-a", []registry.Record{rec(263, "1")})
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			_, ok, err = resumeguard.AliveRecord(f.fake, f.proc, "cs-a", []registry.Record{rec(263, "77")})
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(f.fake.CommandLines()).To(ContainElement(fmt.Sprintf("docker top %s -o pid", "cs-a")))
		})

		It("CS-SESS-106: a failed docker top is an error the caller decides on", func() {
			fake := &execx.Fake{}
			fake.On("docker top", "", errors.New("exit status 1"))
			_, _, err := resumeguard.AliveRecord(fake, f.proc, "cs-a", []registry.Record{rec(263, "1")})
			Expect(err).To(MatchError(ContainSubstring("docker top cs-a")))
		})
	})
})
