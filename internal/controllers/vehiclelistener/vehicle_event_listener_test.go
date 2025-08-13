package vehiclelistener

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
)

func TestSendWebhookNotification_Non200(t *testing.T) {
	// server that always returns 500
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops", 500)
	}))
	defer ts.Close()

	listener := &SignalListener{}
	wh := &models.Trigger{TargetURI: ts.URL, DeveloperLicenseAddress: []byte{}}
	_, err := listener.sendWebhookNotification(context.Background(), wh, &vss.Signal{})
	if err == nil {
		t.Error("expected error on 500 status, got nil")
	}
}

func TestSendWebhookNotification_BadURL(t *testing.T) {
	listener := &SignalListener{}
	wh := &models.Trigger{TargetURI: "http://invalid.localhost:0", DeveloperLicenseAddress: []byte{}}
	_, err := listener.sendWebhookNotification(t.Context(), wh, &vss.Signal{})
	if err == nil {
		t.Error("expected error on invalid URL, got nil")
	}
}
