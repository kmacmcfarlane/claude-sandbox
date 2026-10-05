package sessions_test

// Spec: spec/host-dirs.feature (CS-DIR-011/012) — discovery carries each
// container's peerroot label and bind sources for the peers-root pin.

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

var _ = Describe("discovery for the peers-root pin (CS-DIR-011/012)", func() {
	peerRow := func(name, state, peerRoot, mounts string) string {
		status := "Up 1 hour"
		if state == sessions.StateExited {
			status = "Exited (0) 1 second ago"
		}
		return strings.Join([]string{name, status, "/p", "claude", "", "v1", "", "", "", "3", "",
			state, "2026-09-30 12:00:00 +0000 UTC", "", "", "",
			"", "", "", "", "", peerRoot, "none", mounts}, sep)
	}

	It("CS-DIR-011: rows carry the peerroot label and the split bind sources; an exited --rm row is returned as removing", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join([]string{
			peerRow("a", "running", "/h/.local/state/claude-sandbox-peers", "/h/.local/state/claude-sandbox-peers/sessions,/h/.local/state/claude-sandbox-peers"),
			peerRow("b", sessions.StateExited, "", "/h/.cache/claude-sandbox/peers"),
			peerRow("c", "running", "none", ""),
		}, "\n")+"\n", nil)
		found, removing, err := sessions.DiscoverForLaunch(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(2))
		Expect(found[0].PeerRoot).To(Equal("/h/.local/state/claude-sandbox-peers"))
		Expect(found[0].Mounts).To(Equal([]string{"/h/.local/state/claude-sandbox-peers/sessions", "/h/.local/state/claude-sandbox-peers"}))
		Expect(found[1].PeerRoot).To(Equal("none"))
		Expect(found[1].Mounts).To(BeEmpty())
		Expect(removing).To(HaveLen(1))
		Expect(removing[0].Name).To(Equal("b"))
		Expect(removing[0].Mounts).To(Equal([]string{"/h/.cache/claude-sandbox/peers"}))
		// The same single docker ps, with --no-trunc and {{.Mounts}} last.
		Expect(fake.Calls).To(HaveLen(1))
		line := strings.Join(fake.Calls[0].Args, " ")
		Expect(line).To(ContainSubstring("--no-trunc"))
		Expect(line).To(ContainSubstring(sep + "{{.Mounts}} "))
		// The ordinary listing still leaves the exited --rm row out.
		all, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
	})

	It("CS-DIR-012: a row that predates the label parses with no peer root and no mounts", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", row("old", "Up 1 hour", "/p", "claude", "otter", "v1", "", "", "")+"\n", nil)
		found, _, err := sessions.DiscoverForLaunch(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].PeerRoot).To(BeEmpty())
		Expect(found[0].Mounts).To(BeNil())
	})
})

var _ = Describe("discovery of the terminal label (CS-SESS-091)", func() {
	head := func(name string) []string {
		return []string{name, "Up 1 hour", "/p", "claude", "otter", "v1", "", "", "", "3", "",
			"running", "2026-09-30 12:00:00 +0000 UTC", "", "", "",
			"", "", "", "", "", "none"}
	}

	It("CS-SESS-091: the label is a ps field just before {{.Mounts}}, which stays last; a 24-field row yields both", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join(append(head("a"), "TERMINAL_EMULATOR=JetBrains-JediTerm", "/h/.cache/claude-sandbox/peers"), sep)+"\n", nil)
		found, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].Terminal).To(Equal("TERMINAL_EMULATOR=JetBrains-JediTerm"))
		Expect(found[0].Mounts).To(Equal([]string{"/h/.cache/claude-sandbox/peers"}))
		line := strings.Join(fake.Calls[0].Args, " ")
		Expect(line).To(ContainSubstring(`{{.Label "claude-sandbox.terminal"}}` + sep + "{{.Mounts}} "))
	})

	It("CS-SESS-091: a 23-field row (the format before the label) keeps its Mounts and the legacy pin, with no terminal label", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join(append(head("old"), "/h/.cache/claude-sandbox/peers"), sep)+"\n", nil)
		found, _, err := sessions.DiscoverForLaunch(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].Terminal).To(BeEmpty())
		Expect(found[0].Mounts).To(Equal([]string{"/h/.cache/claude-sandbox/peers"}))
		Expect(found[0].PeerRoot).To(Equal("none"))
	})
})
