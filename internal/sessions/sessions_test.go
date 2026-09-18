package sessions_test

import (
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// sep mirrors the unit separator the package uses between --format fields.
const sep = "\x1f"

// row builds one docker ps --format line.
func row(name, status, project, mode, instance, version, model, hash, inputs string) string {
	return strings.Join([]string{name, status, project, mode, instance, version, model, hash, inputs, "", ""}, sep)
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

var _ = Describe("worktree label (CS-LNCH-044)", func() {
	It("CS-LNCH-044, CS-SESS-010: reads the worktree label as the eleventh ps field", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join([]string{"cs-a", "Up", "/p", "claude", "otter", "v1", "", "", "", "42", "otter"}, sep)+"\n"+
			row("cs-b", "Up", "/p", "claude", "heron", "v1", "", "", "")+"\n", nil)
		fake.On("docker top", "PID COMMAND\n1 claude\n", nil)
		all, err := sessions.DiscoverAll(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0].Worktree).To(Equal("otter"))
		Expect(all[1].Worktree).To(BeEmpty(), "a shared-checkout session")
		b, err := sessions.MarshalJSON(all)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"worktree": "otter"`))
		Expect(strings.Count(string(b), `"worktree"`)).To(Equal(1), "omitted when empty")
	})
})

var _ = Describe("pid classes (CS-PID-004)", func() {
	It("CS-PID-004: reads the pidclass label as the tenth ps field", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join([]string{"cs-a", "Up", "/p", "claude", "otter", "v1", "", "", "", "42", ""}, sep)+"\n", nil)
		fake.On("docker top", "PID COMMAND\n1 claude\n", nil)
		all, err := sessions.DiscoverAll(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(1))
		Expect(all[0].PIDClass).To(Equal("42"))
		Expect(sessions.Classes(all)).To(Equal([]string{"42"}))
		// A container from a launcher that predates classes has none.
		Expect(sessions.Classes([]sessions.Session{{Name: "old"}})).To(BeEmpty())
	})

	It("CS-PID-004: samples a class without replacement across the host", func() {
		first := sessions.PickClass(nil, func(int) int { return 0 })
		Expect(first).To(Equal(0))
		second := sessions.PickClass([]string{"0"}, func(int) int { return 0 })
		Expect(second).To(Equal(1))
		inUse := make([]string, 0, 255)
		for k := 0; k < 255; k++ {
			inUse = append(inUse, strconv.Itoa(k))
		}
		Expect(sessions.PickClass(inUse, func(int) int { return 0 })).To(Equal(255))
		Expect(sessions.PickClass(inUse, func(int) int { return 9999 })).To(Equal(255))
	})

	It("CS-PID-004: when every class is taken any class is returned rather than blocking", func() {
		inUse := make([]string, 0, 256)
		for k := 0; k < 256; k++ {
			inUse = append(inUse, strconv.Itoa(k))
		}
		got := sessions.PickClass(inUse, func(int) int { return 7 })
		Expect(got).To(Equal(7))
		Expect(sessions.PickClass(nil, nil)).To(SatisfyAll(BeNumerically(">=", 0), BeNumerically("<", 256)))
	})
})

// stateRow is a docker ps line carrying the optional State and CreatedAt fields.
func stateRow(name, project, instance, class, state, createdAt string) string {
	status := "Up 1 minute"
	if state == sessions.StateCreated {
		status = "Created"
	}
	return strings.Join([]string{name, status, project, "claude", instance, "v1", "", "", "", class, "", state, createdAt}, sep)
}

var _ = Describe("reservations (CS-SESS-050..052)", func() {
	var fake *execx.Fake
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format("2006-01-02 15:04:05 -0700 MST") }

	BeforeEach(func() { fake = &execx.Fake{} })

	It("CS-SESS-050: discovery lists created as well as running containers, and nothing else", func() {
		fake.On("docker ps", stateRow("a", "/p", "otter", "3", "running", stamp(time.Hour))+"\n"+
			stateRow("b", "/p", "heron", "9", sessions.StateCreated, stamp(time.Second))+"\n", nil)
		fake.On("docker top", "PID COMMAND\n1 claude\n", nil)

		got, err := sessions.DiscoverAll(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(sessions.Instances(got)).To(ConsistOf("otter", "heron"), "a reservation's noun is in use")
		Expect(sessions.Classes(got)).To(ConsistOf("3", "9"), "a reservation's class is in use")
		Expect(fake.CommandLines()[0]).To(ContainSubstring(
			"docker ps -a --filter label=claude-sandbox.project --filter status=created --filter status=running --filter status=paused --format "))
		Expect(got[1].Reserved()).To(BeTrue())
		Expect(got[1].CreatedAt.Equal(now.Add(-time.Second))).To(BeTrue())
		Expect(got[0].Reserved()).To(BeFalse())
	})

	It("CS-SESS-050: a paused container is listed, attachable and holds its class", func() {
		fake.On("docker ps", stateRow("p", "/p", "otter", "5", "paused", stamp(time.Hour))+"\n", nil)
		got, err := sessions.Discover(fake, "/p")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Reserved()).To(BeFalse())
		Expect(sessions.Instances(sessions.Interactive(got))).To(Equal([]string{"otter"}))
		Expect(sessions.Classes(got)).To(Equal([]string{"5"}))
	})

	It("CS-SESS-052: CreatedAt parses in zones whose abbreviation is numeric", func() {
		for _, v := range []string{
			"2026-09-18 22:30:00 +1030 +1030", // Lord Howe
			"2026-09-18 17:45:00 +0545 +0545", // Kathmandu
			"2026-09-18 02:30:00 -0930 -0930", // Marquesas
			"2026-09-18 12:00:00 +0000 UTC",
			"2026-09-18 05:00:00 -0700 PDT",
		} {
			fake = &execx.Fake{}
			fake.On("docker ps", strings.Join([]string{"b", "Created", "/p", "claude", "heron", "v1", "", "", "", "", "", "created", v}, sep)+"\n", nil)
			got, err := sessions.DiscoverAll(fake)
			Expect(err).NotTo(HaveOccurred())
			Expect(got[0].CreatedAt.IsZero()).To(BeFalse(), v)
			Expect(got[0].CreatedAt.Equal(now)).To(BeTrue(), "%s parsed as %s", v, got[0].CreatedAt)
		}
	})

	It("CS-SESS-048: DiscoverAllUncounted runs no docker top", func() {
		fake.On("docker ps", stateRow("a", "/p", "otter", "3", "running", stamp(time.Hour))+"\n", nil)
		got, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(fake.CommandLines()).To(HaveLen(1))
		Expect(fake.CommandLines()[0]).To(HavePrefix("docker ps -a "))
	})

	It("CS-SESS-053: Inspect reads a container's state and RFC 3339 creation time", func() {
		// The exact docker inspect {{.Created}} format: RFC 3339 with nanoseconds.
		fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}} x",
			"created 2026-09-18T18:03:16.123456789Z\n", nil)
		state, created := sessions.Inspect(fake, "x")
		Expect(state).To(Equal(sessions.StateCreated))
		Expect(created.Equal(time.Date(2026, 9, 18, 18, 3, 16, 123456789, time.UTC))).To(BeTrue(), "%s", created)

		fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}} gone", "", execx.Fail(1))
		state, created = sessions.Inspect(fake, "gone")
		Expect(state).To(Equal(""))
		Expect(created.IsZero()).To(BeTrue())

		// The docker ps layout is NOT accepted here: an unparsable time is zero.
		fake.On("docker inspect --type container -f {{.State.Status}} {{.Created}} ps-shaped",
			"created 2026-09-18 18:03:16 +0000 UTC\n", nil)
		state, created = sessions.Inspect(fake, "ps-shaped")
		Expect(state).To(Equal(sessions.StateCreated))
		Expect(created.IsZero()).To(BeTrue())
	})

	It("CS-SESS-050: rows from an older format without State still parse, falling back to Status", func() {
		s := sessions.Session{Status: "Created"}
		Expect(s.Reserved()).To(BeTrue())
		s = sessions.Session{Status: "Up 2 hours"}
		Expect(s.Reserved()).To(BeFalse())
	})

	It("CS-SESS-051: reservations are never candidates, never listed, and never docker-top'd", func() {
		fake.On("docker ps", stateRow("a", "/p", "otter", "", "running", stamp(time.Hour))+"\n"+
			stateRow("b", "/p", "heron", "", sessions.StateCreated, stamp(time.Second))+"\n", nil)
		got, err := sessions.Discover(fake, "/p")
		Expect(err).NotTo(HaveOccurred())
		Expect(sessions.Instances(sessions.Interactive(got))).To(Equal([]string{"otter"}))
		Expect(sessions.Instances(sessions.Live(got))).To(Equal([]string{"otter"}))
		for _, l := range fake.CommandLines() {
			Expect(l).NotTo(Equal("docker top b -o pid,args"))
		}
	})

	It("CS-SESS-052: only reservations older than the limit are stale", func() {
		all := []sessions.Session{
			{Name: "old", State: sessions.StateCreated, CreatedAt: now.Add(-61 * time.Second)},
			{Name: "young", State: sessions.StateCreated, CreatedAt: now.Add(-59 * time.Second)},
			{Name: "unknown-age", State: sessions.StateCreated},
			{Name: "running-old", State: "running", CreatedAt: now.Add(-time.Hour)},
		}
		stale := sessions.Stale(all, now, 60*time.Second)
		Expect(stale).To(HaveLen(1))
		Expect(stale[0].Name).To(Equal("old"))
	})

	It("CS-SESS-052: a reservation is removed with plain docker rm, never forced", func() {
		Expect(sessions.RemoveReservation(fake, "old")).To(Succeed())
		Expect(fake.CommandLines()).To(Equal([]string{"docker rm old"}))
	})

	It("CS-SESS-048: ForProject narrows one host-wide discovery to a project", func() {
		all := []sessions.Session{{Name: "a", Project: "/p"}, {Name: "b", Project: "/q"}}
		Expect(sessions.ForProject(all, "/p")).To(Equal([]sessions.Session{{Name: "a", Project: "/p"}}))
	})
})
