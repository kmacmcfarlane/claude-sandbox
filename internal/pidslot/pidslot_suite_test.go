package pidslot_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPidslot(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pidslot Suite")
}
