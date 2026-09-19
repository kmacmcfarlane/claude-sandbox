package oomreport_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOOMReport(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OOM Report Suite")
}
