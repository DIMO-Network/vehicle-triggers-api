package webhooksender

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
)

// signatureHeaders sign the request body using the trigger's signing_secret.
// Receivers verify with HMAC-SHA256(secret, timestamp + "." + body). The
// timestamp is included to give the receiver a replay window check (out-of-
// order delivery is fine; arbitrary-time replay is not). When the trigger's
// secret is null/empty (legacy rows pre-migration 00006), we skip signing -
// receivers should treat unsigned requests as an error.
func signatureHeaders(secret string, body []byte) (timestamp, signature string) {
	if secret == "" {
		return "", ""
	}
	timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	signature = hex.EncodeToString(mac.Sum(nil))
	return timestamp, signature
}

const (
	// WebhookFailureCode is the code returned when a webhook caused an error
	WebhookFailureCode = -1

	// Default timeout for webhook requests
	defaultWebhookTimeout = 30 * time.Second
	// Maximum response body size to read for error logging
	maxResponseBodySize = 1024
)

// WebhookSender handles all webhook delivery operations
type WebhookSender struct {
	client *http.Client
}

// NewWebhookSender creates a WebhookSender with an http.Client tuned for
// high-throughput webhook dispatch. The default transport is replaced so
// the keep-alive pool can actually sustain bursts to popular receivers -
// Go's MaxIdleConnsPerHost=2 default forces a new TCP+TLS handshake on the
// third concurrent request, which dominates per-fire latency at any real
// production rate. Pass a non-nil client to override (used by tests).
func NewWebhookSender(client *http.Client) *WebhookSender {
	if client == nil {
		client = &http.Client{
			Timeout:   defaultWebhookTimeout,
			Transport: defaultTransport(),
		}
	}
	return &WebhookSender{
		client: client,
	}
}

// defaultTransport returns the production-tuned http.Transport. We keep a
// generous per-host pool because a single popular receiver may handle many
// triggers across many vehicles, all from the same pod.
func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 1024
	t.MaxIdleConnsPerHost = 64
	t.MaxConnsPerHost = 0 // 0 = unlimited; pool will reuse, not block
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 20 * time.Second
	t.ExpectContinueTimeout = 1 * time.Second
	t.ForceAttemptHTTP2 = true
	return t
}

// SendWebhook sends a webhook notification to the specified trigger
// Returns error for failures, nil for success
func (w *WebhookSender) SendWebhook(ctx context.Context, t *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	// Marshal payload
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.TargetURI, bytes.NewBuffer(body))
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return richerrors.Error{
				Code: WebhookFailureCode,
				Err:  fmt.Errorf("invalid URL: %w", err),
			}
		}
		return fmt.Errorf("failed to create webhook request: %w", err)

	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DIMO-Webhook/1.0")
	if t.SigningSecret.Valid && t.SigningSecret.String != "" {
		ts, sig := signatureHeaders(t.SigningSecret.String, body)
		req.Header.Set("X-DIMO-Timestamp", ts)
		req.Header.Set("X-DIMO-Signature", sig)
		req.Header.Set("X-DIMO-Signature-Version", "v1")
	}

	// Send request
	resp, err := w.client.Do(req)
	if err != nil {
		return richerrors.Error{
			Code: WebhookFailureCode,
			Err:  fmt.Errorf("failed to POST to webhook: %w", err),
		}
	}
	defer resp.Body.Close() // nolint:errcheck

	// Check status code
	if resp.StatusCode >= 400 {
		// Read response body for error details (limited size for security)
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
		return richerrors.Error{
			Code: WebhookFailureCode,
			Err:  fmt.Errorf("webhook returned status code %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	return nil
}
