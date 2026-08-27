// spill-e2e validates the large-payload path (ADR 0001/0002) against the live
// TEST compose stack: it streams a large JSON array into the real MinIO spill
// bucket, publishes the ref envelope onto real NATS, and verifies the filter
// and converter stream it through with correct content and an empty DLQ.
//
// Prereqs (see docs/adr/0002-transform-large-payloads.md, Test plan):
//
//	docker compose up -d nats postgres-management minio-test data-filter data-converter
//	docker compose up minio-init
//	seed the e2e connection row (SQL in the ADR), then:
//	go run ./cmd/spill-e2e -mb 1024
//
// Watch transform memory while it runs:
//
//	docker stats vrsky-data-filter vrsky-data-converter
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

const (
	tenant = "e2e-tenant"
	connID = "11111111-2222-3333-4444-555555555555"
)

// recordGen emits a JSON array of records as a stream: the driver itself never
// holds the payload in memory either.
type recordGen struct {
	target  int64 // stop emitting new records past this many bytes
	written int64
	i       int
	buf     []byte
	done    bool
	pad     string
}

func (g *recordGen) Read(p []byte) (int, error) {
	if len(g.buf) == 0 {
		if g.done {
			return 0, io.EOF
		}
		switch {
		case g.i == 0:
			g.buf = []byte("[")
		case g.written >= g.target:
			g.buf = []byte("]")
			g.done = true
		default:
			keep := "no"
			if g.i%2 == 1 {
				keep = "yes"
			}
			sep := ","
			if g.i == 1 {
				sep = ""
			}
			g.buf = []byte(fmt.Sprintf(`%s{"keep":%q,"i":%d,"pad":%q}`, sep, keep, g.i, g.pad))
		}
		g.i++
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	g.written += int64(n)
	return n, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	sizeMB := flag.Int64("mb", 1024, "approximate payload size in MiB")
	flag.Parse()
	ctx := context.Background()

	// Defaults match the TEST compose stack; override via env to target another
	// stack (e.g. prod through port-forwards).
	store, err := objectstore.New(ctx, &objectstore.Config{
		Provider: "s3", Bucket: "vrsky-objects", Region: "us-east-1",
		Endpoint:        envOr("SPILL_E2E_MINIO_ENDPOINT", "http://127.0.0.1:9000"),
		AccessKeyID:     envOr("SPILL_E2E_MINIO_ACCESS_KEY", "minioadmin"),
		SecretAccessKey: envOr("SPILL_E2E_MINIO_SECRET_KEY", "minioadmin"),
	})
	if err != nil {
		log.Fatalf("minio: %v", err)
	}

	env := envelope.New()
	env.TenantID = tenant
	env.IntegrationID = connID
	env.ContentType = "application/json"
	key := fmt.Sprintf("spill/%s/%s/%s", tenant, connID, env.ID)

	// Stream-generate the payload into MinIO, hashing and counting as we go.
	gen := &recordGen{target: *sizeMB << 20, pad: strings.Repeat("x", 1024)}
	h := sha256.New()
	counted := io.TeeReader(gen, h)
	start := time.Now()
	if err := store.PutStream(ctx, key, counted, "application/json"); err != nil {
		log.Fatalf("upload: %v", err)
	}
	env.PayloadRef = key
	env.PayloadSize = gen.written
	env.Checksum = "sha256:" + hex.EncodeToString(h.Sum(nil))
	totalRecords := gen.i - 2 // minus '[' and ']' emissions
	fmt.Printf("UPLOADED  %.1f MiB, %d records, in %s\n", float64(gen.written)/(1<<20), totalRecords, time.Since(start).Round(time.Millisecond))

	// Watch for the transform outputs before publishing.
	nc, err := nats.Connect(envOr("SPILL_E2E_NATS_URL", "nats://127.0.0.1:4222"))
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()

	outFilter := make(chan *envelope.Envelope, 2)
	outConv := make(chan *envelope.Envelope, 2)
	sub, err := nc.Subscribe(messaging.DataSubject(tenant, connID), func(m *nats.Msg) {
		var e envelope.Envelope
		if json.Unmarshal(m.Data, &e) != nil || e.Metadata == nil {
			return
		}
		switch e.Metadata["_last_processed_by"] {
		case "f1":
			outFilter <- &e
		case "cv1":
			outConv <- &e
		}
	})
	if err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	pub := messaging.NewPublisher(js)
	body, _ := json.Marshal(env)
	fmt.Printf("PUBLISHED ref envelope (%d bytes on the bus) at %s\n", len(body), time.Now().Format("15:04:05"))
	if err := pub.Publish(ctx, tenant, connID, env.ID, body); err != nil {
		log.Fatalf("publish: %v", err)
	}
	t0 := time.Now()

	wait := func(name string, ch chan *envelope.Envelope) *envelope.Envelope {
		select {
		case e := <-ch:
			fmt.Printf("%-9s ref=%v inline=%dB size=%.1f MiB checksum=%v after %s\n",
				strings.ToUpper(name), e.PayloadRef != "", len(e.Payload), float64(e.PayloadSize)/(1<<20), e.Checksum != "", time.Since(t0).Round(time.Millisecond))
			return e
		case <-time.After(5 * time.Minute):
			log.Fatalf("TIMEOUT waiting for %s output", name)
			return nil
		}
	}
	fe := wait("filter", outFilter)
	ce := wait("converter", outConv)

	// Validate the converter's NDJSON output by streaming it back.
	if ce.PayloadRef == "" {
		log.Fatalf("FAIL: converter output expected to be spilled, was inline (%d bytes)", len(ce.Payload))
	}
	rc, _, err := store.GetStream(ctx, ce.PayloadRef)
	if err != nil {
		log.Fatalf("read converter output: %v", err)
	}
	defer rc.Close()
	var lines, badLines int
	var firstLine string
	dec := json.NewDecoder(rc)
	for {
		var rec map[string]interface{}
		if derr := dec.Decode(&rec); derr == io.EOF {
			break
		} else if derr != nil {
			log.Fatalf("converter output line %d: %v", lines+1, derr)
		}
		lines++
		if firstLine == "" {
			b, _ := json.Marshal(rec)
			firstLine = string(b)
		}
		if _, hasIdx := rec["index"]; !hasIdx {
			badLines++
		}
		if rec["keep"] != "yes" {
			badLines++
		}
	}

	wantKept := (totalRecords + 1) / 2 // odd indices 1..N
	fmt.Printf("VALIDATE  converter NDJSON: %d lines (want %d), first: %s\n", lines, wantKept, firstLine)
	ok := true
	if lines != wantKept {
		fmt.Printf("FAIL: line count %d != kept records %d\n", lines, wantKept)
		ok = false
	}
	if badLines > 0 {
		fmt.Printf("FAIL: %d lines missing the mapped field or with keep!=yes\n", badLines)
		ok = false
	}
	if fe.PayloadRef == "" {
		fmt.Println("FAIL: filter output expected spilled")
		ok = false
	}

	// DLQ must be empty.
	if entries, derr := messaging.ListDLQ(js, tenant, connID, 10, 0); derr == nil && len(entries) > 0 {
		fmt.Printf("FAIL: DLQ has %d entries\n", len(entries))
		ok = false
	}

	if !ok {
		os.Exit(1)
	}
	fmt.Println("PASS")
}
