package ralphloop_test

// Spec: spec/ralph-quota.feature CS-RQT-013. No real network: the webhook is
// an httptest server.

import (
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kmacmcfarlane/claude-sandbox/internal/ralphloop"
)

var _ = Describe("NotifyDiscord", func() {
	var (
		requests []string
		server   *httptest.Server
	)

	BeforeEach(func() {
		requests = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			requests = append(requests, string(body))
		}))
		DeferCleanup(server.Close)
	})

	It("CS-RQT-013: is a silent no-op when DISCORD_WEBHOOK_URL is unset", func() {
		GinkgoT().Setenv("DISCORD_WEBHOOK_URL", "")
		ralphloop.NotifyDiscord("hello")
		Expect(requests).To(BeEmpty())
	})

	It("CS-RQT-013: posts a JSON {content} message when DISCORD_WEBHOOK_URL is set", func() {
		GinkgoT().Setenv("DISCORD_WEBHOOK_URL", server.URL)
		ralphloop.NotifyDiscord("hello world")
		Expect(requests).To(Equal([]string{`{"content":"hello world"}`}))
	})

	It("CS-RQT-013: webhook failures never affect the caller", func() {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		dead.Close() // connection refused from here on
		GinkgoT().Setenv("DISCORD_WEBHOOK_URL", dead.URL)
		Expect(func() { ralphloop.NotifyDiscord("hello") }).NotTo(Panic())
	})
})
