package imagebuild_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestImagebuild(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Imagebuild Suite")
}
