package metriclistener

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
)

func TestSendWebhookNotification_Non200(t *testing.T) {
	// server that always returns 500
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops", 500)
	}))
	defer ts.Close()

	listener := &MetricListener{}
	wh := &models.Trigger{TargetURI: ts.URL, DeveloperLicenseAddress: []byte{}}
	err := listener.sendWebhookNotification(context.Background(), wh, &cloudevent.CloudEvent[webhook.WebhookPayload]{})
	if err == nil {
		t.Error("expected error on 500 status, got nil")
	}
}

func TestSendWebhookNotification_BadURL(t *testing.T) {
	listener := &MetricListener{}
	wh := &models.Trigger{TargetURI: "http://invalid.localhost:0", DeveloperLicenseAddress: []byte{}}
	err := listener.sendWebhookNotification(t.Context(), wh, &cloudevent.CloudEvent[webhook.WebhookPayload]{})
	if err == nil {
		t.Error("expected error on invalid URL, got nil")
	}
}
