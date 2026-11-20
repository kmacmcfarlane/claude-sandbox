package launch_test

// Spec: spec/launch.feature CS-LNCH-241 — a cascade mount's container path
// that is the container home or an ancestor of it.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/launch"
)

var _ = Describe("CoversHome (CS-LNCH-241)", func() {
	DescribeTable("CS-LNCH-241: compares cleaned paths, / included",
		func(container, home string, want bool) {
			Expect(launch.CoversHome(container, home)).To(Equal(want))
		},
		Entry("the home itself", "/home/rt", "/home/rt", true),
		Entry("the home, trailing slash", "/home/rt/", "/home/rt", true),
		Entry("the home, uncleaned", "/home/./x/../rt", "/home/rt", true),
		Entry("a parent", "/home", "/home/rt", true),
		Entry("the root", "/", "/home/rt", true),
		Entry("the root, doubled", "//", "/home/rt", true),
		Entry("below the home", "/home/rt/.ssh", "/home/rt", false),
		Entry("a sibling sharing a prefix", "/home/r", "/home/rt", false),
		Entry("another path", "/mnt/host-home", "/home/rt", false),
		Entry("a relative path", "home/rt", "/home/rt", false),
		Entry("no home", "/home/rt", "", false),
	)
})
