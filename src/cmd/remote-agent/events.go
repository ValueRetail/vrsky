package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Live events for the pipeline builder's Remote Agent panel, served as SSE at
// /events/{connID}. Like every worker's event stream this endpoint has no auth
// of its own: the builder reaches it only through the management API's
// worker-events proxy, which checks the caller owns the connection. The port is
// not exposed publicly — only /agent is.

type event struct {
	Type       string `json:"type"` // connected | ingested | delivered | failed
	Filename   string `json:"filename,omitempty"`
	EnvelopeID string `json:"envelope_id,omitempty"`
	Message    string `json:"message,omitempty"`
	Time       string `json:"time"`
}

type eventHub struct {
	mu   sync.Mutex
	subs map[string]map[chan event]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[string]map[chan event]struct{}{}} }

func (h *eventHub) subscribe(connID string) (chan event, func()) {
	ch := make(chan event, 32)
	h.mu.Lock()
	if h.subs[connID] == nil {
		h.subs[connID] = map[chan event]struct{}{}
	}
	h.subs[connID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[connID], ch)
		h.mu.Unlock()
	}
}

// emit fans an event out; a slow listener drops events rather than blocking a
// delivery.
func (h *eventHub) emit(connID string, e event) {
	e.Time = time.Now().UTC().Format(time.RFC3339)
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[connID] {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *gateway) handleEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/events/"), "/")
		if connID == "" {
			http.Error(w, "missing connection ID", http.StatusBadRequest)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsub := s.events.subscribe(connID)
		defer unsub()

		hello, _ := json.Marshal(event{Type: "connected", Time: time.Now().UTC().Format(time.RFC3339)})
		fmt.Fprintf(w, "data: %s\n\n", hello)
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-ch:
				data, _ := json.Marshal(e)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
