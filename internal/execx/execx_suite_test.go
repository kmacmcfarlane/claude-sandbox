package execx_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/execx"
)

// The test binary doubles as a launcher: with helperEnv set it runs the
// script in it as a session child and exits with its status. Signal tests
// target that process, not the test process, whose SIGINT and SIGTERM belong
// to Ginkgo's interrupt handler.
const (
	helperEnv = "EXECX_SESSION_HELPER_SCRIPT"
	// helperForeground makes the helper act as the terminal's foreground
	// group (CS-LNCH-086).
	helperForeground = "EXECX_SESSION_HELPER_FOREGROUND"
	// helperLate makes the helper print "returned" once the child exited and
	// then wait up to 3 s for a late signal before releasing (CS-LNCH-097).
	helperLate = "EXECX_SESSION_HELPER_LATE"
	// helperTether makes the helper Start the script with DieWithParent and
	// then block forever (CS-LNCH-098).
	helperTether = "EXECX_SESSION_HELPER_TETHER"
	// helperDetach makes the helper Start the script with Detach and then
	// block forever (CS-IMG-041).
	helperDetach = "EXECX_SESSION_HELPER_DETACH"
)

func TestExecx(t *testing.T) {
	if script, ok := os.LookupEnv(helperEnv); ok {
		os.Exit(runHelper(script))
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Execx Suite")
}

func runHelper(script string) int {
	if os.Getenv(helperTether) == "1" {
		if _, err := (execx.System{}).Start(execx.Cmd{Name: "sh", Args: []string{"-c", script}, Stdout: os.Stdout, DieWithParent: true}); err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			return 99
		}
		select {}
	}
	if os.Getenv(helperDetach) == "1" {
		if _, err := (execx.System{}).Start(execx.Cmd{Name: "sh", Args: []string{"-c", script}, Stdout: os.Stdout, Detach: true}); err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			return 99
		}
		select {}
	}
	if os.Getenv(helperForeground) == "1" {
		execx.SetForeground(func() bool { return true })
	}
	res, err := execx.System{}.RunSession(execx.Cmd{Name: "sh", Args: []string{"-c", script}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		return 99
	}
	defer res.Done()
	if res.Forwarded != nil {
		fmt.Fprintf(os.Stderr, "forwarded=%v\n", res.Forwarded)
	}
	if os.Getenv(helperLate) == "1" {
		fmt.Println("returned")
		select {
		case sig := <-res.Late:
			fmt.Fprintf(os.Stderr, "late=%v\n", sig)
		case <-time.After(3 * time.Second):
			fmt.Fprintln(os.Stderr, "late=none")
		}
	}
	return res.Code
}
