package e2e_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// WebhookReceiver is a mock HTTP server that receives webhook calls
type WebhookReceiver struct {
	server     *httptest.Server
	received   []WebhookCall
	mu         sync.RWMutex
	expectCall chan struct{}
}

type WebhookCall struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Time    time.Time         `json:"time"`
}

func NewWebhookReceiver() *WebhookReceiver {
	wr := &WebhookReceiver{
		received:   make([]WebhookCall, 0),
		expectCall: make(chan struct{}, 1),
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		if string(body) == `{"verification": "test"}` {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`test-verification-token`))
			return
		}

		// Convert headers to map
		headers := make(map[string]string)
		for key, values := range r.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}

		// Record the call
		call := WebhookCall{
			Method:  r.Method,
			URL:     r.URL.String(),
			Headers: headers,
			Body:    string(body),
			Time:    time.Now(),
		}

		wr.mu.Lock()
		wr.received = append(wr.received, call)
		wr.mu.Unlock()

		// Signal that we received a call
		select {
		case wr.expectCall <- struct{}{}:
		default:
		}

		// Return success
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "success"}`))
	}))

	wr.server = server
	return wr
}

func (wr *WebhookReceiver) URL() string {
	return wr.server.URL
}

func (wr *WebhookReceiver) GetReceivedCalls() []WebhookCall {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	result := make([]WebhookCall, len(wr.received))
	copy(result, wr.received)
	return result
}

func (wr *WebhookReceiver) ClearReceivedCalls() {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	wr.received = wr.received[:0]
}

func (wr *WebhookReceiver) WaitForCall(timeout time.Duration) bool {
	select {
	case <-wr.expectCall:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (wr *WebhookReceiver) Close() {
	wr.server.Close()
}
