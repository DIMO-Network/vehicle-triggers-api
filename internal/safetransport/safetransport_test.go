package safetransport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBlockedRanges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},      // loopback
		{"::1", true},            // loopback v6
		{"169.254.169.254", true}, // cloud metadata (link-local)
		{"10.0.0.5", true},       // RFC1918
		{"172.16.0.1", true},     // RFC1918
		{"192.168.1.1", true},    // RFC1918
		{"100.64.0.1", true},     // CGNAT
		{"0.0.0.0", true},        // unspecified
		{"224.0.0.1", true},      // multicast
		{"fc00::1", true},        // ULA
		{"8.8.8.8", false},       // public
		{"1.1.1.1", false},       // public
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := blocked(ip); got != c.blocked {
			t.Errorf("blocked(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// TestGuardRefusesLoopback proves the guarded client refuses to dial a
// loopback server end-to-end (the SSRF control path), not just the unit
// predicate.
func TestGuardRefusesLoopback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := Client(5 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected guarded client to refuse loopback dial, got nil error")
	}
	if !strings.Contains(err.Error(), "refusing to dial") {
		t.Fatalf("expected SSRF refusal, got: %v", err)
	}
}
