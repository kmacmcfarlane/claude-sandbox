package ralphloop

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// NotifyDiscord posts a best-effort message to DISCORD_WEBHOOK_URL; a no-op
// when unset, and failures never affect the loop (CS-RQT-013).
func NotifyDiscord(message string) {
	url := os.Getenv("DISCORD_WEBHOOK_URL")
	if url == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"content": message})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return
	}
	resp.Body.Close()
}
