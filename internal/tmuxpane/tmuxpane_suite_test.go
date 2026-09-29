package tmuxpane_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTmuxpane(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tmuxpane Suite")
}
