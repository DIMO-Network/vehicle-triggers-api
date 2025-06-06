package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestEvaluateCondition(t *testing.T) {
	logger := zerolog.Nop()
	listener := &SignalListener{
		log: logger,
	}

	tests := []struct {
		name      string
		condition string
		telemetry string
		signal    Signal
		want      bool
		wantErr   bool
	}{
		{
			name:      "Empty condition returns true",
			condition: "",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 75,
				ValueString: "foo",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueNumber > 100 false",
			condition: "valueNumber > 100",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 50,
				ValueString: "bar",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "valueNumber > 100 true",
			condition: "valueNumber > 100",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 150,
				ValueString: "baz",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueNumber equals 1 returns true",
			condition: "valueNumber == 1",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 1,
				ValueString: "fop",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueNumber equals 0 returns false",
			condition: "valueNumber == 0",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 0,
				ValueString: "bat",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "Invalid condition returns error",
			condition: "invalid condition",
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 80,
				ValueString: "active",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listener.evaluateCondition(tc.condition, &tc.signal, tc.telemetry)
			if (err != nil) != tc.wantErr {
				t.Errorf("evaluateCondition() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvaluateCondition_StringComparisons(t *testing.T) {
	logger := zerolog.Nop()
	listener := &SignalListener{log: logger}

	tests := []struct {
		name      string
		condition string
		telemetry string
		signal    Signal
		want      bool
		wantErr   bool
	}{
		{
			name:      "valueString == 'Active' true",
			condition: `valueString == 'Active'`,
			telemetry: "valueString",
			signal: Signal{
				ValueNumber: 42,
				ValueString: "Active",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueString == 'Active' false",
			condition: `valueString == 'Active'`,
			telemetry: "valueString",
			signal: Signal{
				ValueString: "Inactive",
				TokenID:     1,
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "mismatched types error",
			condition: `valueString > 'foo'`,
			telemetry: "valueString",
			signal: Signal{
				ValueString: "bar",
				TokenID:     1,
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "unknown variable in expr",
			condition: `foo == 1`,
			telemetry: "valueNumber",
			signal: Signal{
				ValueNumber: 1,
				TokenID:     1,
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listener.evaluateCondition(tc.condition, &tc.signal, tc.telemetry)
			if (err != nil) != tc.wantErr {
				t.Fatalf("evaluateCondition() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSendWebhookNotification_Success(t *testing.T) {
	// start a test server that always returns 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify we get the JSON payload
		var sig Signal
		if err := json.NewDecoder(r.Body).Decode(&sig); err != nil {
			t.Errorf("unexpected body decode error: %v", err)
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	listener := &SignalListener{log: zerolog.Nop()}
	wh := Webhook{URL: ts.URL, DeveloperLicenseAddress: nil}
	err := listener.sendWebhookNotification(wh, &Signal{
		TokenID:     42,
		Timestamp:   time.Now(),
		Name:        "foo",
		ValueNumber: 1.23,
		ValueString: "bar",
	})
	if err != nil {
		t.Errorf("expected no error on 200, got %v", err)
	}
}

func TestSendWebhookNotification_Non200(t *testing.T) {
	// server that always returns 500
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops", 500)
	}))
	defer ts.Close()

	listener := &SignalListener{log: zerolog.Nop()}
	wh := Webhook{URL: ts.URL, DeveloperLicenseAddress: nil}
	err := listener.sendWebhookNotification(wh, &Signal{})
	if err == nil {
		t.Error("expected error on 500 status, got nil")
	}
}

func TestSendWebhookNotification_BadURL(t *testing.T) {
	listener := &SignalListener{log: zerolog.Nop()}
	wh := Webhook{URL: "http://invalid.localhost:0", DeveloperLicenseAddress: nil}
	err := listener.sendWebhookNotification(wh, &Signal{})
	if err == nil {
		t.Error("expected error on invalid URL, got nil")
	}
}
