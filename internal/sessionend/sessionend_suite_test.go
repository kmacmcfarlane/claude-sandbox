package sessionend_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSessionEnd(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session End Suite")
}
