package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestClaudeSandboxCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "claude-sandbox CLI Suite")
}
