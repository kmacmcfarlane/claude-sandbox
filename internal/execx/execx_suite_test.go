package execx_test

import (
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// helperEnv makes the test binary act as a launcher: it runs the script in
// the variable as a session child and exits with its status. Signal tests
// target that process, not the test process, whose SIGINT and SIGTERM belong
// to Ginkgo's interrupt handler.
const helperEnv = "EXECX_SESSION_HELPER_SCRIPT"

func TestExecx(t *testing.T) {
	if script, ok := os.LookupEnv(helperEnv); ok {
		res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", script}})
		if err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			os.Exit(99)
		}
		if res.Forwarded != nil {
			fmt.Fprintf(os.Stderr, "forwarded=%v\n", res.Forwarded)
		}
		os.Exit(res.Code)
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Execx Suite")
}
