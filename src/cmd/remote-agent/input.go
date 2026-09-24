package main

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

// handleUpload takes one file from an agent's read directory into a pipeline.
//
// POST /agent/v1/uploads?connection_id=&directory=&filename=&upload_id=
//
// Accepted only if a running pipeline currently asks THIS agent to watch THAT
// directory. Every other case — no such pipeline, another agent's pipeline,
// another tenant's, a directory not being watched — gets the same 404, so an
// agent cannot probe for pipelines it does not serve. The envelope's tenant is
// the pipeline's, taken from the session, never from the request.
//
// upload_id becomes the envelope ID, which is the JetStream de-duplication key:
// an agent that retries after losing a 2xx response does not publish twice.
func (s *gateway) handleUpload(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	q := r.URL.Query()
	connID, dir, name, uploadID := q.Get("connection_id"), q.Get("directory"), q.Get("filename"), q.Get("upload_id")

	if err := agentproto.ValidFilename(name); err != nil {
		writeErr(w, http.StatusBadRequest, agentproto.ErrInvalidFilename, err.Error())
		return
	}
	if !agentproto.ValidDirectoryName(dir) {
		writeErr(w, http.StatusBadRequest, agentproto.ErrUnknownDirectory, "invalid directory name")
		return
	}
	if _, err := uuid.Parse(uploadID); err != nil {
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, "upload_id must be a UUID")
		return
	}

	sess := s.watchingSession(id, connID, dir)
	if sess == nil {
		writeErr(w, http.StatusNotFound, agentproto.ErrNoActiveWatch,
			"no running pipeline is watching that folder on this agent")
		return
	}

	body := bufio.NewReaderSize(http.MaxBytesReader(w, r.Body, s.uploadMax), 4096)
	head, _ := body.Peek(1)

	env := &envelope.Envelope{
		ID:            uploadID,
		TenantID:      sess.tenantID,
		IntegrationID: sess.connID,
		ContentType:   agentproto.DetectContentType(name, head),
		Source:        "remote-agent:" + id.Name,
		CurrentStep:   0,
		StepHistory:   []string{"remote-agent"},
		CreatedAt:     time.Now().UTC(),
		Metadata: map[string]interface{}{
			"filename":  name,
			"agent_id":  id.ID,
			"directory": dir,
		},
	}

	var err error
	if s.publishStream != nil && (r.ContentLength < 0 || r.ContentLength > int64(s.inlineMax)) {
		err = s.publishStream(r.Context(), env, body)
	} else {
		var data []byte
		if data, err = io.ReadAll(body); err == nil {
			env.Payload = data
			env.PayloadSize = int64(len(data))
			err = s.publish(r.Context(), env)
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, agentproto.ErrTooLarge, "file is larger than this VRSky accepts")
			return
		}
		s.logger.Error("Could not publish upload", "connection_id", sess.connID, "agent_id", id.ID, "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "could not take the file into the pipeline; retry")
		return
	}

	s.events.emit(sess.connID, event{Type: "ingested", Filename: name, EnvelopeID: env.ID})
	writeJSON(w, http.StatusCreated, agentproto.UploadResponse{EnvelopeID: env.ID})
}

// watchingSession returns the running pipeline that has this agent watching
// this directory, or nil. The session's tenant must also be the agent's.
func (s *gateway) watchingSession(id agentIdentity, connID, dir string) *connSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[strings.TrimSpace(connID)]
	if sess == nil || sess.tenantID != id.TenantID {
		return nil
	}
	for _, n := range sess.inputs {
		if n.AgentID == id.ID && n.Directory == dir {
			return sess
		}
	}
	return nil
}
