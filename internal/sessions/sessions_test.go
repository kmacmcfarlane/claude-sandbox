package sessions_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// sep mirrors the unit separator the package uses between --format fields.
const sep = "\x1f"

// row builds one docker ps --format line.
func row(name, status, project, mode, instance, version, model, hash, inputs string) string {
	return strings.Join([]string{name, status, project, mode, instance, version, model, hash, inputs}, sep)
}

var _ = Describe("session discovery", func() {
	var fake *execx.Fake

	BeforeEach(func() { fake = &execx.Fake{} })

	Describe("Discover (CS-SESS-001..006)", func() {
		It("CS-SESS-001: filters by the project label and reads labels from the same ps output", func() {
			fake.On("docker ps", row(
				"claude-sandbox-w-proj-abc123-otter", "Up 2 hours", "/w/proj",
				"claude", "otter", "v1.2.3", "opus", "deadbeef1234", "[]")+"\n", nil)
			fake.On("docker top", "PID   COMMAND\n1  claude\n", nil)

			got, err := sessions.Discover(fake, "/w/proj")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].Name).To(Equal("claude-sandbox-w-proj-abc123-otter"))
			Expect(got[0].Project).To(Equal("/w/proj"))
			Expect(got[0].Instance).To(Equal("otter"))
			Expect(got[0].Model).To(Equal("opus"))
			Expect(got[0].ConfigHash).To(Equal("deadbeef1234"))

			lines := fake.CommandLines()
			Expect(lines[0]).To(ContainSubstring("--filter label=claude-sandbox.project=/w/proj"))
			// One ps for the listing; no per-container inspect.
			Expect(strings.Join(lines, "\n")).NotTo(ContainSubstring("docker inspect"))
		})

		It("CS-SESS-002: DiscoverAll filters on the bare label key", func() {
			fake.On("docker ps", row("a", "Up 1s", "/one", "claude", "otter", "", "", "", "")+"\n"+
				row("b", "Up 2s", "/two", "claude", "heron", "", "", "", "")+"\n", nil)
			got, err := sessions.DiscoverAll(fake)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(2))
			Expect(fake.CommandLines()[0]).To(ContainSubstring("--filter label=claude-sandbox.project "))
		})

		It("CS-SESS-003: counts joined sessions via docker top", func() {
			fake.On("docker ps", row("a", "Up 1s", "/one", "claude", "otter", "", "", "", "")+"\n", nil)
			fake.On("docker top", strings.Join([]string{
				"PID                 COMMAND",
				"3257831             claude",
				"3258484             claude",
				"3258999             claude",
				"3259100             node /some/helper.js",
			}, "\n"), nil)

			got, err := sessions.Discover(fake, "/one")
			Expect(err).NotTo(HaveOccurred())
			Expect(got[0].Count).To(Equal(3), "helper processes must not be counted as sessions")
		})

		It("CS-SESS-004: a failing docker top degrades to zero without failing discovery", func() {
			fake.On("docker ps", row("a", "Up 1s", "/one", "claude", "otter", "", "", "", "")+"\n"+
				row("b", "Up 2s", "/one", "claude", "heron", "", "", "", "")+"\n", nil)
			fake.On("docker top", "", execx.Fail(1))

			got, err := sessions.Discover(fake, "/one")
			Expect(err).NotTo(HaveOccurred(), "a container that is running must still be listed")
			Expect(got).To(HaveLen(2))
			Expect(got[0].Count).To(BeZero())
			Expect(got[1].Count).To(BeZero())
		})

		It("CS-SESS-005: the attachable process is the one whose pid equals State.Pid", func() {
			fake.On("docker inspect", "3257831\n", nil)
			pid, err := sessions.Attachable(fake, "a")
			Expect(err).NotTo(HaveOccurred())
			Expect(pid).To(Equal(3257831))
			Expect(fake.CommandLines()[0]).To(ContainSubstring("{{.State.Pid}}"))
		})

		It("CS-SESS-006: a malformed row is skipped and the rest survive", func() {
			fake.On("docker ps", "not-enough-fields\n"+
				row("b", "Up 2s", "/one", "claude", "heron", "", "", "", "")+"\n", nil)
			got, err := sessions.Discover(fake, "/one")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].Instance).To(Equal("heron"))
		})

		It("CS-SESS-006: label values containing commas and quotes survive the field split", func() {
			// The inputs label is JSON. A naive comma or pipe separator would
			// corrupt it, so the separator must be a character JSON cannot hold.
			inputs := `[{"p":"/w/.claude-sandbox/env","d":"9f8e7d6c","k":"env"}]`
			fake.On("docker ps", row("a", "Up 1s", "/w", "claude", "otter", "v1", "opus", "abc", inputs)+"\n", nil)
			got, err := sessions.Discover(fake, "/w")
			Expect(err).NotTo(HaveOccurred())
			Expect(got[0].Inputs).To(HaveLen(1))
			Expect(got[0].Inputs[0].Path).To(Equal("/w/.claude-sandbox/env"))
		})

		It("returns no sessions when nothing is running", func() {
			fake.On("docker ps", "\n", nil)
			got, err := sessions.Discover(fake, "/w")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty())
		})

		It("surfaces a docker ps failure as an error", func() {
			fake.On("docker ps", "", execx.Fail(1))
			_, err := sessions.Discover(fake, "/w")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("helpers", func() {
		all := []sessions.Session{
			{Name: "a", Instance: "otter", Mode: "claude"},
			{Name: "b", Instance: "heron", Mode: "claude"},
			{Name: "c", Instance: "", Mode: "ralph"},
		}

		It("finds a session by instance noun", func() {
			s, ok := sessions.ByInstance(all, "heron")
			Expect(ok).To(BeTrue())
			Expect(s.Name).To(Equal("b"))
			_, ok = sessions.ByInstance(all, "nosuch")
			Expect(ok).To(BeFalse())
		})

		It("lists instance nouns in use, skipping ralph", func() {
			Expect(sessions.Instances(all)).To(ConsistOf("otter", "heron"))
		})

		It("CS-SESS-034: excludes ralph containers from attach/join candidates", func() {
			Expect(sessions.Interactive(all)).To(HaveLen(2))
		})
	})
})

var _ = Describe("instance nouns (CS-SESS-007..009)", func() {
	It("CS-SESS-007: samples without replacement, so a collision is impossible", func() {
		// Force the chooser to index 0 every time: even the most degenerate
		// chooser cannot return a noun that is already in use, because in-use
		// nouns are removed from the pool before choosing.
		first := sessions.PickNoun(nil, func(int) int { return 0 })
		second := sessions.PickNoun([]string{first}, func(int) int { return 0 })
		third := sessions.PickNoun([]string{first, second}, func(int) int { return 0 })
		Expect(second).NotTo(Equal(first))
		Expect(third).NotTo(Equal(first))
		Expect(third).NotTo(Equal(second))
	})

	It("CS-SESS-007: never returns a noun already in use", func() {
		inUse := append([]string{}, sessions.Nouns[:10]...)
		for i := 0; i < 50; i++ {
			got := sessions.PickNoun(inUse, func(n int) int { return i % n })
			Expect(inUse).NotTo(ContainElement(got))
		}
	})

	It("CS-SESS-008: falls back to a suffix when every noun is in use", func() {
		got := sessions.PickNoun(sessions.Nouns, func(int) int { return 0 })
		Expect(got).To(Equal(sessions.Nouns[0] + "-2"))
	})

	It("CS-SESS-008: keeps incrementing the suffix while those are taken too", func() {
		inUse := append(append([]string{}, sessions.Nouns...), sessions.Nouns[0]+"-2", sessions.Nouns[0]+"-3")
		got := sessions.PickNoun(inUse, func(int) int { return 0 })
		Expect(got).To(Equal(sessions.Nouns[0] + "-4"))
	})

	It("CS-SESS-009: a nil chooser still works (real randomness)", func() {
		Expect(sessions.PickNoun(nil, nil)).To(BeElementOf(sessions.Nouns))
	})

	It("CS-SESS-009: tolerates an out-of-range chooser rather than panicking", func() {
		Expect(sessions.PickNoun(nil, func(int) int { return 9999 })).To(BeElementOf(sessions.Nouns))
		Expect(sessions.PickNoun(nil, func(int) int { return -1 })).To(BeElementOf(sessions.Nouns))
	})

	It("ignores whitespace when comparing in-use nouns", func() {
		got := sessions.PickNoun([]string{" " + sessions.Nouns[0] + " "}, func(int) int { return 0 })
		Expect(got).NotTo(Equal(sessions.Nouns[0]))
	})
})
