package tmuxpane_test

// Spec: spec/tmux.feature CS-TMUX-080 — the pane marks a restart unsets
// before its stop and sets back after a failed one; spec/sessions.feature
// CS-SESS-107 — the session family a restart strips from a passthrough.

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
	"github.com/kmacmcfarlane/claude-sandbox/internal/tmuxpane"
)

var _ = Describe("a restart's pane marks (CS-TMUX-080)", func() {
	const id = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	mark := func(container, cid string) tmuxpane.Mark {
		return tmuxpane.Mark{V: tmuxpane.MarkVersion, State: tmuxpane.StateActive, Mode: tmuxpane.ModeClaude,
			Container: container, ContainerID: cid, Project: "/w/p", Worktree: ""}
	}

	It("CS-TMUX-080: lists the panes whose mark names the container, by id, else by name", func() {
		fake := &execx.Fake{}
		byID := mark("dunlin", id)
		byName := mark("dunlin", "")
		other := mark("other", strings.Repeat("c", 64))
		sameNameOtherID := mark("dunlin", strings.Repeat("d", 64))
		fake.On("list-panes", strings.Join([]string{
			"%1\t" + byID.JSON(), "%2\t" + byName.JSON(), "%3\t" + other.JSON(), "%4\t" + sameNameOtherID.JSON(),
			"%5\t", "%6\tnot json", "garbage",
		}, "\n")+"\n", nil)
		held := tmuxpane.MarksFor(fake, id, "dunlin")
		var panes []string
		for _, h := range held {
			panes = append(panes, h.Pane)
		}
		Expect(panes).To(Equal([]string{"%1", "%2"}))
		Expect(held[0].Raw).To(Equal(byID.JSON()))
		Expect(fake.CommandLines()[0]).To(Equal("tmux list-panes -a -F #{pane_id}\t#{@claude-sandbox}"))
	})

	It("CS-TMUX-080: a tmux that does not answer lists nothing", func() {
		fake := &execx.Fake{}
		fake.On("list-panes", "", execx.Fail(1))
		Expect(tmuxpane.MarksFor(fake, id, "dunlin")).To(BeEmpty())
	})

	It("CS-TMUX-080: unsets each held pane's mark; puts one back only on a pane still unset", func() {
		fake := &execx.Fake{}
		m := mark("dunlin", id)
		held := []tmuxpane.HeldMark{{Pane: "%1", Raw: m.JSON()}, {Pane: "%2", Raw: m.JSON()}}
		tmuxpane.UnsetMarks(fake, held)
		Expect(fake.CommandLines()).To(Equal([]string{
			"tmux set-option -p -u -t %1 @claude-sandbox", "tmux set-option -p -u -t %2 @claude-sandbox",
		}))

		fake = &execx.Fake{}
		fake.On("show-options -p -q -v -t %2", mark("new", "").JSON()+"\n", nil)
		tmuxpane.PutBackMarks(fake, held)
		Expect(fake.CommandLines()).To(ContainElement("tmux set-option -p -t %1 @claude-sandbox " + m.JSON()))
		Expect(fake.CommandLines()).NotTo(ContainElement(HavePrefix("tmux set-option -p -t %2")))
	})
})

var _ = Describe("the session family a restart strips (CS-SESS-107)", func() {
	const id = "3f2a9c1e-7b4d-4e8f-9a0b-1c2d3e4f5a6b"
	DescribeTable("CS-SESS-107: StripSessionFamily drops the session family and its values, keeps the rest in order",
		func(in, want []string) {
			Expect(tmuxpane.StripSessionFamily(in)).To(Equal(want))
		},
		Entry("nothing", nil, nil),
		Entry("resume and name", []string{"--resume", id, "--name", "x", "--effort", "high"}, []string{"--effort", "high"}),
		Entry("the = and short forms", []string{"--resume=" + id, "-n", "x", "-r" + id, "--name=y", "--verbose"}, []string{"--verbose"}),
		Entry("a value that looks like a flag's stays with its flag", []string{"--append-system-prompt", "--name", "--bare"}, []string{"--append-system-prompt", "--name", "--bare"}),
		Entry("a positional prompt stops the walk; it is kept", []string{"--name", "x", "do this", "--name", "y"}, []string{"do this", "--name", "y"}),
		Entry("-- stops the walk", []string{"--", "--resume", id}, []string{"--", "--resume", id}),
		Entry("variadic values are kept", []string{"--add-dir", "/a", "/b", "--resume", id}, []string{"--add-dir", "/a", "/b"}),
	)
})
