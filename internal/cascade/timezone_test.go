package cascade_test

// Spec: CS-CASC-050 (the timezone key). The launcher's resolution of the value
// is CS-LNCH-213..217, tested in internal/launch.

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/cascade"
)

var _ = Describe("timezone key (CS-CASC-050)", func() {
	load := func(contents ...string) *cascade.Config {
		snap := make([]cascade.ConfigFile, 0, len(contents))
		for i, c := range contents {
			snap = append(snap, cascade.ConfigFile{Path: "/lvl" + string(rune('0'+i)) + "/config.yaml", Content: []byte(c)})
		}
		cfg, err := cascade.LoadSnapshot(snap)
		Expect(err).NotTo(HaveOccurred())
		return cfg
	}

	DescribeTable("CS-CASC-050: yaml booleans and numbers arrive as text through the snapshot merge",
		func(line, want string) {
			cfg := load(line + "\n")
			Expect(cfg.Timezone).To(HaveValue(Equal(want)))
		},
		Entry("unquoted false", "timezone: false", "false"),
		Entry("FALSE is lowercased by the merge's round trip", "timezone: FALSE", "false"),
		Entry("no", "timezone: no", "no"),
		Entry("off", "timezone: off", "off"),
		Entry("none", "timezone: none", "none"),
		Entry(`quoted "0"`, `timezone: "0"`, "0"),
		Entry("UTC keeps its case", "timezone: UTC", "UTC"),
		Entry("Europe/Berlin keeps its text", "timezone: Europe/Berlin", "Europe/Berlin"),
	)

	It("CS-CASC-050: a more-local level beats an upstream one", func() {
		cfg := load("timezone: Europe/Berlin\n", "timezone: UTC\n")
		Expect(cfg.Timezone).To(HaveValue(Equal("UTC")))
		cfg = load("timezone: UTC\n", "model: sonnet\n")
		Expect(cfg.Timezone).To(HaveValue(Equal("UTC")))
	})

	It("CS-CASC-050: absent is nil, and the key is outside the merged config's JSON", func() {
		Expect(load("model: sonnet\n").Timezone).To(BeNil())
		raw, err := json.Marshal(load("timezone: UTC\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring("UTC"))
		Expect(string(raw)).NotTo(ContainSubstring("imezone"))
	})
})
