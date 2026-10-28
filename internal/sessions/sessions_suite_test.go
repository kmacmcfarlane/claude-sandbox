package sessions_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/sessions"
)

func TestSessions(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sessions Suite")
}

// Discovery's dropped-row warnings (CS-SESS-118) go to the spec's own output
// unless a spec captures them.
var _ = BeforeEach(func() {
	sessions.WarnOut = GinkgoWriter
	DeferCleanup(func() { sessions.WarnOut = os.Stderr })
})
