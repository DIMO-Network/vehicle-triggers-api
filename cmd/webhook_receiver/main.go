package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// WebhookPayload represents a generic payload received from the vehicle events API.
type WebhookPayload struct {
	ID          string      `json:"id"`
	Source      string      `json:"source"`
	Subject     string      `json:"subject"`
	SpecVersion string      `json:"specversion"`
	Time        string      `json:"time"`
	Type        string      `json:"type"`
	Data        interface{} `json:"data"`
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	var payload WebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	log.Printf("Webhook received: %+v\n", payload)
	w.WriteHeader(http.StatusOK)
}

func main() {
	http.HandleFunc("/webhook", webhookHandler)
	log.Println("Webhook receiver listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}
