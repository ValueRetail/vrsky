package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte("Only POST allowed"))
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		msg := string(body)

		fmt.Printf("[Mock HTTP Server] Received POST: %s\n", msg)

		// Log to file so bash script can verify
		f, err := os.OpenFile("/tmp/received-messages.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Printf("[Mock HTTP Server] Error writing to file: %v\n", err)
		} else {
			f.WriteString(msg + "\n")
			f.Close()
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:         ":9001",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Auto-shutdown after 30 seconds
	go func() {
		time.Sleep(30 * time.Second)
		server.Close()
	}()

	log.Println("[Mock HTTP Server] Listening on :9001")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[Mock HTTP Server] Error: %v", err)
	}
	log.Println("[Mock HTTP Server] Shut down")
}
