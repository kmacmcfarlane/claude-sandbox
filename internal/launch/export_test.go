package launch

import "os"

// SetStatPeerSource swaps the pin's stat seam for a test and returns the
// restore (CS-DIR-011).
func SetStatPeerSource(f func(string) (os.FileInfo, error)) func() {
	old := statPeerSource
	statPeerSource = f
	return func() { statPeerSource = old }
}

// CaptureFingerprintText hands every hashed non-file text configFingerprint
// builds to f and returns the restore (CS-LNCH-189).
func CaptureFingerprintText(f func(string)) func() {
	old := fingerprintTextHook
	fingerprintTextHook = f
	return func() { fingerprintTextHook = old }
}

// EscapeBidi exposes the inputs label's bidi escaping (CS-LNCH-226).
var EscapeBidi = escapeBidi
