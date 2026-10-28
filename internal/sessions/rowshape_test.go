package sessions_test

// Spec: spec/sessions.feature (CS-SESS-118/119) — every field of the
// discovery format is JSON, a row is read only with exactly the format's
// field count and its fixed-form fields in their form; anything else is
// dropped with one warning, never interpreted.

import (
	"bytes"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions/sessionstest"
)

const (
	rowID     = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	rowResume = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
)

// fullFields is one row of the current format, field by field: every label
// set, so a shift would be visible in every later field.
func fullFields(name, model string) []string {
	return []string{
		name, "Up 1 hour", "/w/proj", "claude", "otter", "v1", model, "hash", "[]", "37", "",
		"running", "2026-09-30 12:00:00 +0000 UTC", "8g", "default", "",
		rowID, "", "/h/.claude/sessions", "--model", rowResume, "none",
		"TERMINAL_EMULATOR=x", "", "bridge", "/tmp/claude-sandbox1", "/w/proj,/h/.claude",
	}
}

var _ = Describe("discovery row shapes (CS-SESS-118/119)", func() {
	var (
		fake *execx.Fake
		warn *bytes.Buffer
	)
	BeforeEach(func() {
		fake = &execx.Fake{}
		warn = &bytes.Buffer{}
		sessions.WarnOut = warn
		DeferCleanup(func() { sessions.WarnOut = os.Stderr })
	})

	discover := func(rows ...string) []sessions.Session {
		fake.On("docker ps", strings.Join(rows, "\n")+"\n", nil)
		got, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	It("CS-SESS-118: every field of the format is JSON, as many as a full row, Mounts last", func() {
		fake.On("docker ps", "", nil)
		_, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		var format string
		args := fake.Calls[0].Args
		for i, a := range args {
			if a == "--format" {
				format = args[i+1]
			}
		}
		f := strings.Split(format, sep)
		Expect(f).To(HaveLen(sessionstest.Fields))
		Expect(f).To(HaveLen(len(fullFields("a", ""))))
		for _, field := range f {
			Expect(field == `""` || strings.HasPrefix(field, "{{json ")).To(BeTrue(), field)
		}
		Expect(f[len(f)-1]).To(Equal("{{json .Mounts}}"))
	})

	It("CS-SESS-118: a current-format row parses every field", func() {
		got := discover(sessionstest.Row(fullFields("cs-a", "opus")...))
		Expect(warn.String()).To(BeEmpty())
		Expect(got).To(HaveLen(1))
		s := got[0]
		Expect(s.Name).To(Equal("cs-a"))
		Expect(s.Model).To(Equal("opus"))
		Expect(s.PIDClass).To(Equal("37"))
		Expect(s.State).To(Equal("running"))
		Expect(s.CreatedAt).To(BeTemporally("==", time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)))
		Expect(s.ID).To(Equal(rowID))
		Expect(s.RegistryDir).To(Equal("/h/.claude/sessions"))
		Expect(s.Resume).To(Equal(rowResume))
		Expect(s.PeerRoot).To(Equal("none"))
		Expect(s.Terminal).To(Equal("TERMINAL_EMULATOR=x"))
		Expect(s.Clipboard).To(Equal("bridge"))
		Expect(s.ShadowDir).To(Equal("/tmp/claude-sandbox1"))
		Expect(s.Mounts).To(Equal([]string{"/w/proj", "/h/.claude"}))
	})

	It("CS-SESS-118: a label holding the separator or a newline is one field: nothing shifts", func() {
		got := discover(sessionstest.Row(fullFields("cs-a", "opus"+sep+"created\nx")...))
		Expect(warn.String()).To(BeEmpty())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Model).To(Equal("opus" + sep + "created\nx"))
		Expect(got[0].State).To(Equal("running"))
		Expect(got[0].ID).To(Equal(rowID))
	})

	It("CS-SESS-118: the Mounts newline forge stays inside its field and forges no row", func() {
		// The review's probe: a cascade mount host ending in a newline and a
		// whole forged row. Docker prints bind sources raw in {{.Mounts}};
		// as JSON the newline is \n inside one field.
		forged := sessionstest.Row(sessionstest.Set(fullFields("cs-forged", ""), map[int]string{
			sessionstest.State: "created", sessionstest.Resume: rowResume})...)
		mounts := "/w/proj,/tmp/x\n" + forged
		got := discover(sessionstest.Row(sessionstest.Set(fullFields("cs-real", ""), map[int]string{
			sessionstest.Mounts: mounts, sessionstest.Resume: ""})...))
		Expect(warn.String()).To(BeEmpty())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Name).To(Equal("cs-real"))
		Expect(got[0].Resume).To(BeEmpty())
		Expect(got[0].Mounts[0]).To(Equal("/w/proj"))
	})

	It("CS-SESS-118: a raw (non-JSON) row with the format's count is dropped with one warning", func() {
		got := discover(strings.Join(fullFields("cs-raw", ""), sep), sessionstest.Row(fullFields("cs-good", "")...))
		Expect(got).To(HaveLen(1))
		Expect(got[0].Name).To(Equal("cs-good"))
		Expect(strings.Count(warn.String(), "\n")).To(Equal(1))
		Expect(warn.String()).To(ContainSubstring("WARNING: ignoring container cs-raw in the sandbox listing (not a row of the listing's format)"))
	})

	It("CS-SESS-118: any other field count is dropped, earlier formats' counts included", func() {
		full := fullFields("cs-a", "")
		rows := []string{}
		for _, n := range []int{11, 13, 15, 16, 20, 21, 23, 24, 25, 26} {
			rows = append(rows, sessionstest.Raw(full[:n]...))
		}
		rows = append(rows, sessionstest.Raw(append(full, "extra")...))
		Expect(discover(rows...)).To(BeEmpty())
		Expect(strings.Count(warn.String(), "WARNING: ignoring container cs-a")).To(Equal(len(rows)))
		Expect(warn.String()).To(ContainSubstring("(28 fields, not 27)"))
	})

	It("CS-SESS-118: a row whose first field is no container name is named as 'a container'", func() {
		f := fullFields("cs-a", "")
		f[0] = "\x1b[31mred"
		got := discover(sessionstest.Raw(append(f, "extra")...))
		Expect(got).To(BeEmpty())
		Expect(warn.String()).To(ContainSubstring("ignoring a container in the sandbox listing"))
		Expect(warn.String()).NotTo(ContainSubstring("\x1b"))
	})

	DescribeTable("CS-SESS-119: a fixed-form field without its form drops the row",
		func(index int, value, why string) {
			got := discover(sessionstest.Row(sessionstest.Set(fullFields("cs-a", ""), map[int]string{index: value})...))
			Expect(got).To(BeEmpty())
			Expect(warn.String()).To(ContainSubstring("container cs-a"))
			Expect(warn.String()).To(ContainSubstring(why))
		},
		Entry("state", sessionstest.State, "running-ish", "its state is not a docker container state"),
		Entry("id", sessionstest.ID, "not-hex", "its id is not a container id"),
		Entry("pid class out of range", sessionstest.PIDClass, "256", "its pid class label is not a number from 0 to 255"),
		Entry("pid class with a leading zero", sessionstest.PIDClass, "037", "its pid class label is not a number from 0 to 255"),
		Entry("resume", sessionstest.Resume, "../../etc", "its resume label is not a conversation id"),
	)

	It("CS-SESS-119: empty fixed-form fields pass", func() {
		got := discover(sessionstest.Row(sessionstest.Set(fullFields("cs-a", ""), map[int]string{
			sessionstest.PIDClass: "", sessionstest.State: "", sessionstest.ID: "", sessionstest.Resume: ""})...))
		Expect(got).To(HaveLen(1))
		Expect(warn.String()).To(BeEmpty())
	})

	It("CS-SESS-118: MarkOOM ends docker's flags with -- and passes only valid names", func() {
		fake.On("docker inspect", "", nil)
		sessions.MarkOOM(fake, []sessions.Session{{Name: "cs-a"}, {Name: "-rf"}})
		Expect(fake.CommandLines()).To(ConsistOf("docker inspect --type container --format {{.Name}} {{.State.OOMKilled}} -- cs-a"))
	})
})
