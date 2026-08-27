package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

type sftpProbe struct {
	inline     *envelope.Envelope
	streamed   *envelope.Envelope
	streamBody []byte
}

func (p *sftpProbe) publish(_ context.Context, env *envelope.Envelope) error {
	p.inline = env
	return nil
}

func (p *sftpProbe) publishStream(_ context.Context, env *envelope.Envelope, body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	p.streamed, p.streamBody = env, b
	return nil
}

func newStreamingSFTPConsumer(probe *sftpProbe, inlineMax int, streaming bool) *sftpConsumer {
	c := &sftpConsumer{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		publish:   probe.publish,
		inlineMax: inlineMax,
	}
	if streaming {
		c.publishStream = sdk.PublishStreamFunc(probe.publishStream)
	}
	return c
}

func TestSFTPFetchAndPublish_LargeFileStreams(t *testing.T) {
	const inlineMax = 32
	payload := bytes.Repeat([]byte("L"), inlineMax*10)
	fake := &fakeSFTP{files: map[string][]byte{"big.csv": payload}}
	probe := &sftpProbe{}
	c := newStreamingSFTPConsumer(probe, inlineMax, true)

	streamed, err := c.fetchAndPublish(context.Background(), fake, "conn-1", "tenant-x",
		remoteFile{Name: "big.csv", Size: int64(len(payload))}, "/in/big.csv")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if !streamed {
		t.Error("a file over the inline threshold should stream")
	}
	if probe.inline != nil {
		t.Error("the buffered publish path must not be used for a streamed file")
	}
	if !bytes.Equal(probe.streamBody, payload) {
		t.Errorf("streamed %d bytes, want %d", len(probe.streamBody), len(payload))
	}
	if probe.streamed.ContentType != "text/csv" {
		t.Errorf("ContentType = %q, want text/csv", probe.streamed.ContentType)
	}
	if probe.streamed.ID == "" {
		t.Error("streamed envelope needs an ID (it keys the spill object)")
	}
	if probe.streamed.Metadata["filename"] != "big.csv" {
		t.Errorf("metadata lost: %+v", probe.streamed.Metadata)
	}
}

func TestSFTPFetchAndPublish_SmallFileStaysInline(t *testing.T) {
	payload := []byte(`{"id":1}`)
	fake := &fakeSFTP{files: map[string][]byte{"small.json": payload}}
	probe := &sftpProbe{}
	c := newStreamingSFTPConsumer(probe, 1024, true)

	streamed, err := c.fetchAndPublish(context.Background(), fake, "conn-1", "tenant-x",
		remoteFile{Name: "small.json", Size: int64(len(payload))}, "/in/small.json")
	if err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if streamed || probe.streamed != nil {
		t.Error("a file under the inline threshold must not stream")
	}
	if probe.inline == nil || !bytes.Equal(probe.inline.Payload, payload) {
		t.Error("expected the file published inline, intact")
	}
}

// No payload store (or offload disabled) means the buffered path, unchanged.
func TestSFTPFetchAndPublish_FallsBackWithoutStreaming(t *testing.T) {
	payload := bytes.Repeat([]byte("B"), 4096)
	for _, tc := range []struct {
		name      string
		inlineMax int
		streaming bool
	}{
		{"no payload store", 32, false},
		{"offload disabled", 0, true},
		{"negative threshold", -8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSFTP{files: map[string][]byte{"big.bin": payload}}
			probe := &sftpProbe{}
			c := newStreamingSFTPConsumer(probe, tc.inlineMax, tc.streaming)

			streamed, err := c.fetchAndPublish(context.Background(), fake, "conn-1", "tenant-x",
				remoteFile{Name: "big.bin", Size: int64(len(payload))}, "/in/big.bin")
			if err != nil {
				t.Fatalf("fetchAndPublish: %v", err)
			}
			if streamed {
				t.Error("must not stream")
			}
			if probe.inline == nil || !bytes.Equal(probe.inline.Payload, payload) {
				t.Error("expected the buffered path to publish the whole file")
			}
		})
	}
}
