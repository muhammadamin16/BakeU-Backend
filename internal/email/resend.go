// Package email sends transactional email via the Resend HTTP API.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const resendEndpoint = "https://api.resend.com/emails"

// Mailer sends email through Resend. When the API key is empty (e.g. local dev),
// Send logs the message instead of calling the network, so flows still work.
type Mailer struct {
	apiKey string
	from   string
	http   *http.Client
}

func NewMailer(apiKey, from string) *Mailer {
	return &Mailer{
		apiKey: apiKey,
		from:   from,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

type sendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

// Send delivers an HTML email to the given recipients.
func (m *Mailer) Send(ctx context.Context, to []string, subject, html string) error {
	if len(to) == 0 {
		return nil
	}
	if m.apiKey == "" {
		log.Printf("[email] (no RESEND_API_KEY) would send to %v: %q", to, subject)
		return nil
	}

	body, err := json.Marshal(sendRequest{From: m.from, To: to, Subject: subject, HTML: html})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("resend: %s: %s", resp.Status, string(msg))
	}
	return nil
}
