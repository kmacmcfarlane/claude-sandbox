package ralphloop_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRalphloop(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ralphloop Suite")
}
