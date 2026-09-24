package hostdirs_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHostdirs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hostdirs Suite")
}
