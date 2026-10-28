package sessions_test

// Spec: spec/host-dirs.feature (CS-DIR-011/012) — discovery carries each
// container's peerroot label and bind sources for the peers-root pin.

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions/sessionstest"
)

var _ = Describe("discovery for the peers-root pin (CS-DIR-011/012)", func() {
	peerRow := func(name, state, peerRoot, mounts string) string {
		status := "Up 1 hour"
		if state == sessions.StateExited {
			status = "Exited (0) 1 second ago"
		}
		return sessionstest.Row(name, status, "/p", "claude", "", "v1", "", "", "", "3", "",
			state, "2026-09-30 12:00:00 +0000 UTC", "", "", "",
			"", "", "", "", "", peerRoot, "none", "", "", "", mounts)
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
		Expect(line).To(ContainSubstring(sep + "{{json .Mounts}} "))
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

	It("CS-SESS-091: the label is a ps field just before {{.Mounts}}, which stays last", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", sessionstest.Row(append(head("a"), "TERMINAL_EMULATOR=JetBrains-JediTerm", "", "", "", "/h/.cache/claude-sandbox/peers")...)+"\n", nil)
		found, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(1))
		Expect(found[0].Terminal).To(Equal("TERMINAL_EMULATOR=JetBrains-JediTerm"))
		Expect(found[0].Mounts).To(Equal([]string{"/h/.cache/claude-sandbox/peers"}))
		line := strings.Join(fake.Calls[0].Args, " ")
		// CS-LNCH-182's continue label sits between them now.
		Expect(line).To(ContainSubstring(`{{json (.Label "claude-sandbox.terminal")}}` + sep + `{{json (.Label "claude-sandbox.continue")}}` + sep))
	})

	It("CS-SESS-091, CS-SESS-118: a row of an earlier format's count is not read", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", sessionstest.Raw(append(head("old"), "/h/.cache/claude-sandbox/peers")...)+"\n", nil)
		found, _, err := sessions.DiscoverForLaunch(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeEmpty())
	})
})

var _ = Describe("discovery of the continue label (CS-LNCH-182)", func() {
	head := func(name string) []string {
		return []string{name, "Up 1 hour", "/p", "claude", "otter", "v1", "", "", "", "3", "",
			"running", "2026-09-30 12:00:00 +0000 UTC", "", "", "",
			"", "", "", "", "", "none", "none"}
	}

	It("CS-LNCH-182: the label is a ps field just before {{.Mounts}}; \"1\" sets it, anything else does not", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join([]string{
			sessionstest.Row(append(head("a"), "1", "", "", "/m1")...),
			sessionstest.Row(append(head("b"), "", "", "", "/m2")...),
			sessionstest.Row(append(head("c"), "yes", "", "", "/m3")...),
		}, "\n")+"\n", nil)
		found, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(3))
		Expect([]bool{found[0].Continue, found[1].Continue, found[2].Continue}).To(Equal([]bool{true, false, false}))
		Expect(found[0].Terminal).To(Equal("none"))
		Expect(found[0].Mounts).To(Equal([]string{"/m1"}))
		line := strings.Join(fake.Calls[0].Args, " ")
		Expect(line).To(ContainSubstring(`{{json (.Label "claude-sandbox.continue")}}` + sep + `{{json (.Label "claude-sandbox.clipboard")}}` + sep + `{{json (.Label "claude-sandbox.shadowdir")}}` + sep + "{{json .Mounts}} "))
	})

	It("CS-LNCH-197: the clipboard and shadowdir labels sit before {{.Mounts}}", func() {
		fake := &execx.Fake{}
		fake.On("docker ps", strings.Join([]string{
			sessionstest.Row(append(head("a"), "", "bridge", "/tmp/claude-sandbox1", "/m1")...),
			sessionstest.Row(append(head("b"), "", "", "", "/m2")...),
		}, "\n")+"\n", nil)
		found, err := sessions.DiscoverAllUncounted(fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(2))
		Expect(found[0].Clipboard).To(Equal("bridge"))
		Expect(found[0].ShadowDir).To(Equal("/tmp/claude-sandbox1"))
		Expect(found[0].Mounts).To(Equal([]string{"/m1"}))
		Expect(found[1].Clipboard).To(BeEmpty())
		Expect(found[1].ShadowDir).To(BeEmpty())
		Expect(found[1].Mounts).To(Equal([]string{"/m2"}))
	})
})
