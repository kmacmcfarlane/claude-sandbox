package sessions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

// writeRecord writes one registry record <pid>.json into dir.
func writeRecord(dir string, pid int, started time.Time, name, domain string) {
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	b, err := json.Marshal(map[string]any{
		"pid": pid, "sessionId": "0b5e9c3a-1f2d-4e5f-8a9b-0c1d2e3f4a5b", "cwd": "/w/proj",
		"startedAt": started.UnixMilli(), "name": name, "nameSource": "user", "pidDomain": domain,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), b, 0o600)).To(Succeed())
}

var _ = Describe("friendly session names (CS-SESS-114, CS-SESS-117)", func() {
	var (
		base    string
		created time.Time
	)

	BeforeEach(func() {
		base = GinkgoT().TempDir()
		created = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	})

	sess := func(name, class, registry string) sessions.Session {
		return sessions.Session{Name: name, PIDClass: class, CreatedAt: created, RegistryDir: registry, State: "running"}
	}

	It("CS-SESS-114: reads the label's dir, matches the class and creation, and takes the earliest record", func() {
		reg := filepath.Join(base, "peers", "sessions")
		writeRecord(reg, 7, created.Add(-time.Hour), "leftover", "")                   // an earlier container's
		writeRecord(reg, 263, created.Add(2*time.Second), "primary name", "")          // 263 % 256 == 7
		writeRecord(reg, 519, created.Add(10*time.Minute), "a join", "")               // a later join
		writeRecord(reg, 8, created.Add(time.Second), "another class", "")             // class 8
		writeRecord(reg, 775, created.Add(time.Second), "host claude", "linux:id:pid") // the host's own
		got := sessions.SessionNames([]sessions.Session{sess("cs-a", "7", reg)},
			sessions.NameLookup{OwnDomain: func() string { return "linux:id:pid" }})
		Expect(got).To(Equal(map[string]string{"cs-a": "primary name"}))
	})

	It("CS-SESS-114: a container without the registry label reads the fallback dirs", func() {
		cfg := filepath.Join(base, "home", ".claude", "sessions")
		legacy := filepath.Join(base, "home", ".cache", "claude-sandbox", "peers", "sessions")
		writeRecord(legacy, 12, created.Add(time.Second), "from legacy", "")
		got := sessions.SessionNames([]sessions.Session{sess("cs-a", "12", "")},
			sessions.NameLookup{FallbackDirs: []string{cfg, legacy}})
		Expect(got).To(Equal(map[string]string{"cs-a": "from legacy"}))
	})

	It("CS-SESS-114: a record's startedAt within the creation second counts (truncated)", func() {
		reg := filepath.Join(base, "sessions")
		created = created.Add(700 * time.Millisecond)
		writeRecord(reg, 3, created.Add(-500*time.Millisecond), "same second", "")
		got := sessions.SessionNames([]sessions.Session{sess("cs-a", "3", reg)}, sessions.NameLookup{})
		Expect(got).To(HaveKeyWithValue("cs-a", "same second"))
	})

	It("CS-SESS-114: what cannot be read gives no name, silently", func() {
		reg := filepath.Join(base, "sessions")
		Expect(os.MkdirAll(reg, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(reg, "5.json"), []byte(`{"pid":5,`), 0o600)).To(Succeed())
		writeRecord(reg, 6, created.Add(time.Second), "", "")
		link := filepath.Join(base, "linked")
		Expect(os.Symlink(reg, link)).To(Succeed())
		got := sessions.SessionNames([]sessions.Session{
			sess("malformed", "5", reg),
			sess("unnamed", "6", reg),
			sess("missing-dir", "6", filepath.Join(base, "nope")),
			sess("symlinked-dir", "6", link),
			sess("no-class", "", reg),
			{Name: "no-created", PIDClass: "6", RegistryDir: reg, State: "running"},
		}, sessions.NameLookup{})
		Expect(got).To(BeEmpty())
	})

	It("CS-SESS-114: the own pid domain is asked only when a record was read", func() {
		asked := 0
		lk := sessions.NameLookup{OwnDomain: func() string { asked++; return "" }}
		sessions.SessionNames([]sessions.Session{sess("cs-a", "7", filepath.Join(base, "none"))}, lk)
		Expect(asked).To(Equal(0))
		reg := filepath.Join(base, "sessions")
		writeRecord(reg, 7, created.Add(time.Second), "n", "")
		sessions.SessionNames([]sessions.Session{sess("cs-a", "7", reg), sess("cs-b", "7", reg)}, lk)
		Expect(asked).To(Equal(1))
	})

	It("CS-SESS-117: a shown name is printable and capped at 40 runes", func() {
		Expect(sessions.DisplayName("big refactor")).To(Equal("big refactor"))
		Expect(sessions.DisplayName("a\x1b[31mb‮c​d")).To(Equal("a [31mbcd"))
		long := strings.Repeat("é", 45)
		got := sessions.DisplayName(long)
		Expect([]rune(got)).To(HaveLen(40))
		Expect(got).To(HaveSuffix("…"))
		Expect(sessions.DisplayName(strings.Repeat("x", 40))).To(Equal(strings.Repeat("x", 40)))
	})
})
