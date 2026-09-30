package launch

import "os"

// SetStatPeerSource swaps the pin's stat seam for a test and returns the
// restore (CS-DIR-011).
func SetStatPeerSource(f func(string) (os.FileInfo, error)) func() {
	old := statPeerSource
	statPeerSource = f
	return func() { statPeerSource = old }
}
