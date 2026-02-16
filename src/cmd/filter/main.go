package main

import (
	"log"
	"os"

	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://nats.vrsky-platform:4222"
	}

	log.Printf("Filter service starting. Connecting to NATS at %s\n", natsURL)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	log.Println("✓ Connected to NATS")
	log.Println("Filter placeholder running. Listening for messages...")
	log.Println("TODO: Implement your filter logic here")

	// Keep the service running
	select {}
}
