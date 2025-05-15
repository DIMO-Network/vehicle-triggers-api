package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
)

func handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fmt.Printf("✅ Webhook receiver got payload: %s\n", body)
	// on registration we sent “testtoken” as our verification_token
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(200)
	w.Write([]byte("testtoken"))
}

func main() {
	http.HandleFunc("/", handler)
	log.Println("Listening on http://localhost:4001 …")
	log.Fatal(http.ListenAndServe(":4001", nil))
}
