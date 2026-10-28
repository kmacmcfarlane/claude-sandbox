package main

// Spec: spec/sessions.feature CS-SESS-114..117 — the friendly session name,
// read from the primary claude's registry record, beside the instance noun.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/prompt"
)

// writeNameRecord writes one registry record <pid>.json into dir.
func writeNameRecord(dir string, pid int, started time.Time, name string) {
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	b, err := json.Marshal(map[string]any{
		"pid": pid, "sessionId": guardID, "cwd": "/w", "startedAt": started.UnixMilli(),
		"name": name, "nameSource": "user", "pidDomain": "linux::pid:[1]",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), b, 0o600)).To(Succeed())
}

var _ = Describe("friendly session names (CS-SESS-114..117)", func() {
	var (
		f       *cliFixture
		created time.Time
		peers   string
		psOut   string
	)

	BeforeEach(func() {
		f = newCLIFixture()
		created = time.Now().Add(-time.Hour).Truncate(time.Second)
		peers = filepath.Join(f.home, ".local", "state", "claude-sandbox-peers", "sessions")
		// otter: bridged (registry label), named; heron: no label (fallback
		// to <config dir>/sessions), unnamed; kite: a 60-rune name with an
		// escape sequence in it.
		writeNameRecord(peers, 263, created.Add(2*time.Second), "big refactor")
		writeNameRecord(peers, 519, created.Add(time.Minute), "a later join")
		writeNameRecord(filepath.Join(f.home, ".claude", "sessions"), 9, created.Add(time.Second), "")
		writeNameRecord(peers, 11, created.Add(time.Second), "\x1b]0;evil\x07"+strings.Repeat("long name ", 6))
		psOut = strings.Join([]string{
			psRowResume("cs-otter", f.proj, "claude", "otter", "7", "running", created, peers, ""),
			psRowResume("cs-heron", f.proj, "claude", "heron", "9", "running", created, "", ""),
			psRowResume("cs-kite", f.proj, "claude", "kite", "11", "running", created, peers, ""),
		}, "\n") + "\n"
		f.fake.OnFunc("docker ps", func(execx.Cmd) (string, error) { return psOut, nil })
		f.fake.On("docker top", "PID  COMMAND\n1  claude\n", nil)
	})

	It("CS-SESS-115: the tier-1 list shows each name beside its noun", func() {
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"q"}}
		Expect(f.run()).To(Equal(0))
		out := f.errw.String()
		GinkgoWriter.Println(out)
		Expect(out).To(MatchRegexp(`(?m)^  otter \(big refactor\) +up 1 hour +1 session\(s\)$`))
		Expect(out).To(MatchRegexp(`(?m)^  heron +up 1 hour +1 session\(s\)$`))
		Expect(out).To(ContainSubstring("kite (]0;evil long name long name long name l…)"))
		Expect(out).NotTo(ContainSubstring("\x1b"))
		Expect(out).NotTo(ContainSubstring("a later join"), "the primary's record, not a join's")
	})

	It("CS-SESS-115: the only-candidate pick line names it; printed commands use the noun only", func() {
		psOut = psRowResume("cs-otter", f.proj, "claude", "otter", "7", "running", created, peers, "") + "\n"
		f.env.Prompter = &prompt.Scripted{IsTTY: true, Answers: []string{"a"}}
		Expect(f.run()).To(Equal(0))
		Expect(f.errw.String()).To(ContainSubstring("Using the only running session: otter (big refactor)"))
		Expect(f.sessionLine()).NotTo(ContainSubstring("big refactor"))
	})

	It("CS-SESS-115: with no terminal, the list is printed and the suggested flags name nouns", func() {
		Expect(f.run("--attach")).To(Equal(exitDecisionRequired))
		Expect(f.errw.String()).To(ContainSubstring("otter (big refactor)"))
		Expect(f.errw.String()).To(ContainSubstring("--attach=otter."))
	})

	It("CS-SESS-116: the sessions table has NAME and CONTAINER columns", func() {
		Expect(f.run("sessions")).To(Equal(0))
		out := f.out.String()
		GinkgoWriter.Println(out)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		Expect(strings.Fields(lines[0])).To(Equal([]string{"INSTANCE", "NAME", "WORKTREE", "CONTAINER", "MODE", "STATE", "UP", "SESSIONS"}))
		Expect(lines[1]).To(MatchRegexp(`^otter +big refactor +- +cs-otter +claude +running`))
		Expect(lines[2]).To(MatchRegexp(`^heron +- +- +cs-heron +claude +running`))
		// Columns line up in runes after a name holding "…".
		Expect(strings.Index(lines[3], "cs-kite")).To(BeNumerically(">", 0))
		// No docker call per container beyond the listing's own.
		for _, l := range f.fake.CommandLines() {
			Expect(l).To(Or(HavePrefix("docker ps"), HavePrefix("docker top"), HavePrefix("docker inspect")))
		}
	})

	It("CS-SESS-116, CS-SESS-117: --json carries sessionName, name stays the container", func() {
		Expect(f.run("sessions", "--json")).To(Equal(0))
		var rows []map[string]any
		Expect(json.Unmarshal(f.out.Bytes(), &rows)).To(Succeed())
		Expect(rows).To(HaveLen(3))
		Expect(rows[0]["name"]).To(Equal("cs-otter"))
		Expect(rows[0]["sessionName"]).To(Equal("big refactor"))
		Expect(rows[1]).NotTo(HaveKey("sessionName"))
		Expect([]rune(rows[2]["sessionName"].(string))).To(HaveLen(40))
	})
})
