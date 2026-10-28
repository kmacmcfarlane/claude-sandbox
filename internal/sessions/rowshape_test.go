package sessions_test

// Spec: spec/sessions.feature (CS-SESS-118/119) — a docker ps row is read
// only when its field count is a known shape and its fixed-form fields have
// their form; anything else is dropped with one warning, never interpreted.

import (
	"bytes"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

const (
	rowID     = "4f1c2a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170615e4d3"
	rowResume = "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b"
)

// fullFields is one row of the current format, field by field: every label
// set, so a shift is visible in every later field.
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

	It("CS-SESS-118: the format has exactly as many fields as a full row, Mounts last", func() {
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
		Expect(f).To(HaveLen(len(fullFields("a", ""))))
		Expect(f[len(f)-1]).To(Equal("{{.Mounts}}"))
	})

	It("CS-SESS-118: a current-format row parses every field as before", func() {
		got := discover(strings.Join(fullFields("cs-a", "opus"), sep))
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

	It("CS-SESS-118: a model label holding the separator shifts nothing: the row is dropped with one warning naming the container", func() {
		// The probe: a model of "opus<US>created" would have made every
		// later field one to the right — the state read "created", the id
		// a pid class, the registry a config dir, and so on.
		injected := strings.Join(fullFields("cs-evil", "opus"+sep+"x"), sep)
		got := discover(injected, strings.Join(fullFields("cs-good", "opus"), sep))
		Expect(got).To(HaveLen(1))
		Expect(got[0].Name).To(Equal("cs-good"))
		Expect(strings.Count(warn.String(), "\n")).To(Equal(1))
		Expect(warn.String()).To(ContainSubstring("WARNING: ignoring container cs-evil in the sandbox listing (28 fields, not 27)"))
	})

	It("CS-SESS-118: a row whose first field is no container name is named as 'a container'", func() {
		f := fullFields("cs-a", "")
		f[0] = "\x1b[31mred"
		got := discover(strings.Join(append(f, "extra"), sep))
		Expect(got).To(BeEmpty())
		Expect(warn.String()).To(ContainSubstring("ignoring a container in the sandbox listing"))
		Expect(warn.String()).NotTo(ContainSubstring("\x1b"))
	})

	It("CS-SESS-118: a count between the known shapes is dropped; an earlier shape is read with its own fields", func() {
		full := fullFields("cs-a", "")
		got := discover(
			strings.Join(full[:22], sep),                                         // 22: never a format
			strings.Join(full[:26], sep),                                         // 26: never a format
			strings.Join(full[:21], sep),                                         // 21: the resume format
			row("cs-old", "Up 1s", "/w/proj", "claude", "heron", "", "", "", ""), // 11
		)
		Expect(got).To(HaveLen(2))
		Expect(got[0].Resume).To(Equal(rowResume))
		Expect(got[0].Mounts).To(BeEmpty())
		Expect(got[1].Name).To(Equal("cs-old"))
		Expect(strings.Count(warn.String(), "WARNING: ignoring container cs-a")).To(Equal(2))
	})

	DescribeTable("CS-SESS-119: a fixed-form field without its form drops the row",
		func(index int, value, why string) {
			f := fullFields("cs-a", "")
			f[index] = value
			got := discover(strings.Join(f, sep))
			Expect(got).To(BeEmpty())
			Expect(warn.String()).To(ContainSubstring("container cs-a"))
			Expect(warn.String()).To(ContainSubstring(why))
		},
		Entry("state", 11, "running-ish", "its state is not a docker container state"),
		Entry("id", 16, "not-hex", "its id is not a container id"),
		Entry("pid class out of range", 9, "256", "its pid class label is not a number from 0 to 255"),
		Entry("pid class with a leading zero", 9, "037", "its pid class label is not a number from 0 to 255"),
		Entry("resume", 20, "../../etc", "its resume label is not a conversation id"),
	)

	It("CS-SESS-119: empty fixed-form fields pass", func() {
		f := fullFields("cs-a", "")
		f[9], f[11], f[16], f[20] = "", "", "", ""
		Expect(discover(strings.Join(f, sep))).To(HaveLen(1))
		Expect(warn.String()).To(BeEmpty())
	})
})
