package agent

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// fakeGateway speaks the agent protocol from memory, recording what the agent
// sends. It stands in for cmd/remote-agent, whose own tests cover the server
// side; these cover the agent's half of the conversation.
type fakeGateway struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	credential string
	watches    []agentproto.Watch
	deliveries []agentproto.Delivery
	bodies     map[string]string // delivery ID → body for BodyURL deliveries
	announces  []agentproto.AnnounceRequest
	uploads    []fakeUpload
	acks       map[string]agentproto.AckRequest
	failNext   int    // answer this many requests with 503
	revoked    bool   // answer everything with 403 agent_revoked
	uploadErr  int    // status for uploads (0 = 201)
	uploadCode string // error code with uploadErr
	polls      int
}

type fakeUpload struct {
	ConnectionID, Directory, Filename, UploadID, Body string
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{t: t, credential: agentproto.CredentialPrefix + "test", bodies: map[string]string{}, acks: map[string]agentproto.AckRequest{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(agentproto.ErrorResponse{Error: code, Message: code})
	}
	if g.failNext > 0 {
		g.failNext--
		fail(http.StatusServiceUnavailable, "unavailable")
		return
	}
	if g.revoked {
		fail(http.StatusForbidden, agentproto.ErrAgentRevoked)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+g.credential || r.Header.Get(agentproto.HeaderProto) != "1" {
		fail(http.StatusUnauthorized, agentproto.ErrUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/agent/v1/announce":
		var a agentproto.AnnounceRequest
		_ = json.NewDecoder(r.Body).Decode(&a)
		g.announces = append(g.announces, a)
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/agent/v1/work":
		g.polls++
		resp := agentproto.WorkResponse{Watches: g.watches, WatchesVersion: watchVersion(g.watches), Deliveries: g.deliveries}
		if resp.Watches == nil {
			resp.Watches = []agentproto.Watch{}
		}
		g.deliveries = nil
		// Hold briefly when idle, like the real gateway, so the loop does not spin.
		if len(resp.Deliveries) == 0 && r.URL.Query().Get("watches") == resp.WatchesVersion {
			g.mu.Unlock()
			<-r.Context().Done()
			g.mu.Lock()
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	case strings.HasSuffix(r.URL.Path, "/body"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/agent/v1/deliveries/"), "/body")
		_, _ = io.WriteString(w, g.bodies[id])
	case strings.HasSuffix(r.URL.Path, "/ack"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/agent/v1/deliveries/"), "/ack")
		var a agentproto.AckRequest
		_ = json.NewDecoder(r.Body).Decode(&a)
		g.acks[id] = a
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/agent/v1/uploads":
		if g.uploadErr != 0 {
			fail(g.uploadErr, g.uploadCode)
			return
		}
		b, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		g.uploads = append(g.uploads, fakeUpload{q.Get("connection_id"), q.Get("directory"), q.Get("filename"), q.Get("upload_id"), string(b)})
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(agentproto.UploadResponse{EnvelopeID: q.Get("upload_id")})
	default:
		fail(http.StatusNotFound, "not_found")
	}
}

func watchVersion(ws []agentproto.Watch) string {
	b, _ := json.Marshal(ws)
	return base64.StdEncoding.EncodeToString(b)
}

func (g *fakeGateway) snapshot() (uploads []fakeUpload, acks map[string]agentproto.AckRequest, announces int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	acks = map[string]agentproto.AckRequest{}
	for k, v := range g.acks {
		acks[k] = v
	}
	return append([]fakeUpload(nil), g.uploads...), acks, len(g.announces)
}

func (g *fakeGateway) set(fn func(g *fakeGateway)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fn(g)
}
