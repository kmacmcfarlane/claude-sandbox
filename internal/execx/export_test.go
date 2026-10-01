package execx

import "time"

// SetForeground replaces the foreground-of-terminal check (CS-LNCH-086) for a
// helper process that must behave as if its terminal delivered a signal.
func SetForeground(f func() bool) { inForeground = f }

// ForegroundOfTTY is the real check, for tests of its no-terminal answer.
var ForegroundOfTTY = foregroundOfTTY

// BoundedTerm exposes bounded with a SIGTERM grace (Git's path, CS-LNCH-176)
// for a real-process test that need not be named git.
func BoundedTerm(r Runner, timeout, grace time.Duration, c Cmd) (string, error) {
	return bounded(r, timeout, grace, c)
}
