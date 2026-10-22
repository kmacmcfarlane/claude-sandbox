package clipboard_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestClipboard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Clipboard Suite")
}

// shortDir is a scratch directory under /tmp, never TMPDIR: a unix socket
// path must stay under 108 bytes whatever TMPDIR is (CLAUDE.md).
func shortDir() string {
	d, err := os.MkdirTemp("/tmp", "cbt")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(os.RemoveAll, d)
	return d
}
