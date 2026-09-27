package globalcfg_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGlobalcfg(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Globalcfg Suite")
}
