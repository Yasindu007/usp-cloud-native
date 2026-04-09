package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type entry struct {
	ReceivedAt string            `json:"received_at"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Attempt    int               `json:"attempt"`
}

func main() {
	logDir := filepath.Join(".", ".tmp")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.Fatal(err)
	}
	logPath := filepath.Join(logDir, "webhook-receiver-log.jsonl")
	_ = os.Remove(logPath)

	var mu sync.Mutex
	attempts := map[string]int{}

	handler := func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		bodyBytes, _ := io.ReadAll(r.Body)

		mu.Lock()
		attempts[r.URL.Path]++
		attempt := attempts[r.URL.Path]
		mu.Unlock()

		e := entry{
			ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			Headers: map[string]string{
				"X-Webhook-ID":       r.Header.Get("X-Webhook-ID"),
				"X-Webhook-Delivery": r.Header.Get("X-Webhook-Delivery"),
				"X-Webhook-Event":    r.Header.Get("X-Webhook-Event"),
				"X-Webhook-Signature": r.Header.Get("X-Webhook-Signature"),
				"User-Agent":         r.Header.Get("User-Agent"),
				"Content-Type":       r.Header.Get("Content-Type"),
			},
			Body:    string(bodyBytes),
			Attempt: attempt,
		}

		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_ = json.NewEncoder(f).Encode(e)
			_ = f.Close()
		}

		statusCode := http.StatusOK
		resp := map[string]any{"status": "ok", "attempt": attempt}
		if r.URL.Path == "/fail-once" && attempt == 1 {
			statusCode = http.StatusInternalServerError
			resp = map[string]any{"status": "fail_once", "attempt": attempt}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(resp)
	}

	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServe(":9092", nil))
}
