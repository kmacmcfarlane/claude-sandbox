package resumeguard_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestResumeGuard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Resume Guard Suite")
}
