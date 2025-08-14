package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type mockIdentityServer struct {
	server    *httptest.Server
	responses map[string]map[string]any // request payload hash -> response
	mu        sync.RWMutex
}

func setupIdentityServer(*testing.T) *mockIdentityServer {
	m := &mockIdentityServer{
		responses: make(map[string]map[string]any),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Read and hash the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reqMap := make(map[string]any)
		err = json.Unmarshal(body, &reqMap)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reqBytes, err := json.Marshal(reqMap)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Get response for this exact request payload
		m.mu.RLock()
		response, exists := m.responses[string(reqBytes)]
		m.mu.RUnlock()

		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if err = json.NewEncoder(w).Encode(response); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
	}))

	m.server = server
	return m
}

// SetRequestResponse sets a response for an exact request payload
func (m *mockIdentityServer) SetRequestResponse(request string, response map[string]any) error {
	reqMap := make(map[string]any)
	err := json.Unmarshal([]byte(request), &reqMap)
	if err != nil {
		return err
	}
	reqBytes, err := json.Marshal(reqMap)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[string(reqBytes)] = response
	return nil
}

func (m *mockIdentityServer) URL() string {
	return m.server.URL
}

func (m *mockIdentityServer) Close() {
	m.server.Close()
}
