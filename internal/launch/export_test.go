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

// PrepareAWSMountPoints runs awsPrepareMountPoints alone over the given
// volumes (CS-LNCH-234), so a test can point the Getuid seam at ~/.aws
// without it refusing the scope folders first (which Build checks before the
// mount points). It returns whether they passed and the mode they left.
func PrepareAWSMountPoints(in Inputs, volumes []string, awsDir string, homeMounted bool) (bool, string) {
	if in.Err == nil {
		in.Err = os.Stderr
	}
	p := &Plan{Volumes: volumes}
	st := &awsState{on: true, dir: awsDir, homeMounted: homeMounted, mode: AWSCacheSandbox}
	ok := in.awsPrepareMountPoints(p, st)
	return ok, st.mode
}
