package execx

// SetForeground replaces the foreground-of-terminal check (CS-LNCH-086) for a
// helper process that must behave as if its terminal delivered a signal.
func SetForeground(f func() bool) { inForeground = f }

// ForegroundOfTTY is the real check, for tests of its no-terminal answer.
var ForegroundOfTTY = foregroundOfTTY
