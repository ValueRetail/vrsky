package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

// isDelimited reports whether the file is a delimited table (CSV/TSV) we can
// derive a column schema from.
func isDelimited(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".csv" || ext == ".tsv"
}

func delimiterFor(filename string) rune {
	if strings.EqualFold(filepath.Ext(filename), ".tsv") {
		return '\t'
	}
	return ','
}

// parseDelimitedSample reads the header row (column names) and the first data
// row (values) from CSV/TSV bytes. Returns the headers and a header→value map
// for the first row (empty-string values when there are no data rows), so the
// UI renders fields with sensible string previews.
func parseDelimitedSample(payload []byte, delim rune) ([]string, map[string]string) {
	rd := csv.NewReader(bytes.NewReader(payload))
	rd.Comma = delim
	rd.FieldsPerRecord = -1 // tolerate ragged rows
	rd.LazyQuotes = true

	header, err := rd.Read()
	if err != nil || len(header) == 0 {
		return nil, nil
	}
	// Normalise headers to unique, non-empty names so duplicate/blank columns
	// don't collide into one schema field (or React key) or overwrite row values.
	seen := make(map[string]int, len(header))
	for i := range header {
		name := strings.TrimSpace(header[i])
		if name == "" {
			name = fmt.Sprintf("column_%d", i+1)
		}
		if n := seen[name]; n > 0 {
			seen[name] = n + 1
			name = fmt.Sprintf("%s_%d", name, n+1)
		} else {
			seen[name] = 1
		}
		header[i] = name
	}

	row := make(map[string]string, len(header))
	for _, h := range header {
		row[h] = ""
	}
	if rec, err := rd.Read(); err == nil {
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
	}
	return header, row
}

// HTTP handlers served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9200 in
// compose). Registered in Configure via RegisterHTTPHandler. /health is served
// separately by the SDK on HEALTH_PORT.

// handleUpload accepts a multipart file upload for an active connection and
// publishes its contents into the pipeline.
func (s *fileConsumer) handleUpload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract connectionId from path
		connectionID := strings.TrimPrefix(r.URL.Path, "/upload/")
		connectionID = strings.TrimSuffix(connectionID, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}

		ac := s.getActiveConnection(connectionID)
		if ac == nil {
			http.Error(w, "Connection not found or not running", http.StatusNotFound)
			return
		}

		// Parse multipart form (32MB max)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "Failed to parse upload", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "No file in request", http.StatusBadRequest)
			return
		}
		defer file.Close()

		data, err := io.ReadAll(io.LimitReader(file, 32<<20))
		if err != nil {
			http.Error(w, "Failed to read file", http.StatusInternalServerError)
			return
		}

		filename := header.Filename

		if s.publish == nil {
			// Run() has not yet injected the publish closure (should not happen
			// once a connection is active, but guard rather than panic).
			http.Error(w, "Consumer not ready", http.StatusServiceUnavailable)
			return
		}
		env, err := s.ingestFile(r.Context(), ac, filename, data, "upload:"+filename)
		if err != nil {
			s.logger.Error("Failed to publish uploaded file", "error", err)
			http.Error(w, "Failed to process upload", http.StatusInternalServerError)
			return
		}

		s.emitEvent(ac.ConnectionID, FileEvent{
			Type: "uploaded", Filename: filename, Size: int64(len(data)),
			Time: time.Now().UTC().Format(time.RFC3339),
		})

		s.logger.Info("File uploaded and published",
			"connection_id", ac.ConnectionID,
			"filename", filename,
			"size", len(data),
			"envelope_id", env.ID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		// Encode rather than Sprintf — the filename is user-controlled and may
		// contain quotes/newlines that would otherwise produce invalid JSON.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":      "accepted",
			"filename":    filename,
			"envelope_id": env.ID,
		})
	}
}

// ingestFile builds an envelope from file bytes, publishes it into the
// pipeline, and caches it as last_payload. Shared by the HTTP upload handler
// and the directory watcher so both ingest paths behave identically (the
// watcher previously only detected files and never published them — #143).
func (s *fileConsumer) ingestFile(ctx context.Context, ac *ActiveConnection, filename string, data []byte, source string) (*envelope.Envelope, error) {
	if s.publish == nil {
		return nil, fmt.Errorf("consumer not ready: publish func not injected")
	}
	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		Payload:       data,
		PayloadSize:   int64(len(data)),
		ContentType:   detectContentType(filename, data),
		Source:        source,
		CurrentStep:   0,
		StepHistory:   []string{"file-consumer"},
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]interface{}{"filename": filename},
	}
	if err := s.publish(ctx, env); err != nil {
		return nil, err
	}
	// Cache last payload (the SDK publish path marshals the envelope itself;
	// envelope.Marshal == json.Marshal — identical bytes).
	if envData, mErr := json.Marshal(env); mErr == nil {
		_, _ = s.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", envData, ac.ConnectionID)
	}
	return env, nil
}

// handleEvents is an SSE endpoint that streams file activity events.
func (s *fileConsumer) handleEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		connectionID := strings.TrimPrefix(r.URL.Path, "/events/")
		connectionID = strings.TrimSuffix(connectionID, "/")
		if connectionID == "" {
			http.Error(w, "Missing connection ID", http.StatusBadRequest)
			return
		}

		ac := s.getActiveConnection(connectionID)
		if ac == nil {
			http.Error(w, "Connection not found or not running", http.StatusNotFound)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsub := s.subscribeEvents(connectionID)
		defer unsub()

		// Send initial connected event. Marshal rather than interpolate — the
		// watch dir may contain quotes/control chars that would break the JSON.
		connected, _ := json.Marshal(map[string]string{"type": "connected", "message": "Watching " + ac.WatchDir})
		fmt.Fprintf(w, "data: %s\n\n", connected)
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// handleSampleData returns a small preview of the most recently-modified file
// in the watch directory, used by the UI's filter/converter preview before a
// pipeline is deployed. Phase 1H (#73).
//
// Request:  POST {"path":"/dir"}   or   GET ?path=/dir
// Response: {"ok":true,"data":<parsed JSON | {text:"..."}>,"filename":"…"}
//
//	{"ok":false,"error":"…"}
//
// Path validation: the directory must be under the configured BaseDir or
// under HOST_HOME (same allowlist as the file-producer's file manager).
func (s *fileConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var dirPath string
		switch r.Method {
		case http.MethodGet:
			dirPath = r.URL.Query().Get("path")
		case http.MethodPost:
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
				writeSampleErr(w, "Invalid request body")
				return
			}
			dirPath = body.Path
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if dirPath == "" {
			writeSampleErr(w, "path is required")
			return
		}
		if !s.isSamplePathAllowed(dirPath) {
			s.logger.Warn("Sample data request blocked", "path", dirPath)
			writeSampleErr(w, "path not allowed")
			return
		}

		filename, payload, err := pickSampleFile(dirPath)
		if err != nil {
			writeSampleErr(w, err.Error())
			return
		}

		resp := map[string]interface{}{"ok": true, "filename": filename}
		// Prefer parsed JSON when the content looks like JSON; for CSV/TSV parse
		// the header row into fields (so schema discovery shows columns); fall
		// back to a plain text envelope for everything else.
		ct := detectContentType(filename, payload)
		switch {
		case ct == "application/json":
			var parsed interface{}
			if err := json.Unmarshal(payload, &parsed); err == nil {
				resp["data"] = parsed
			} else {
				resp["data"] = map[string]string{"text": string(payload)}
			}
		case isDelimited(filename):
			headers, firstRow := parseDelimitedSample(payload, delimiterFor(filename))
			if len(headers) > 0 {
				resp["columns"] = headers
				resp["data"] = firstRow // first data row keyed by header (empty strings if no rows)
			} else {
				resp["data"] = map[string]string{"text": string(payload), "content_type": ct}
			}
		default:
			resp["data"] = map[string]string{"text": string(payload), "content_type": ct}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// pickSampleFile lists dir, drops hidden entries and sub-directories, sorts
// by modification time (most recent first) and reads up to 256KB from the
// winner. Returns the filename + bytes.
func pickSampleFile(dir string) (string, []byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("directory does not exist: %s", dir)
		}
		return "", nil, err
	}
	type candidate struct {
		name    string
		path    string
		modTime time.Time
	}
	var cands []candidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, candidate{
			name:    name,
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}
	if len(cands) == 0 {
		return "", nil, fmt.Errorf("no readable files in %s", dir)
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].modTime.After(cands[j].modTime)
	})
	chosen := cands[0]

	f, err := os.Open(chosen.path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256*1024))
	if err != nil {
		return "", nil, err
	}
	return chosen.name, data, nil
}

// isSamplePathAllowed mirrors the file-producer's path allowlist: any path
// under the configured BaseDir or under HOST_HOME is permitted.
func (s *fileConsumer) isSamplePathAllowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Directory may not yet exist; that's a separate error surfaced
		// from pickSampleFile.
		resolved = abs
	}
	roots := []string{s.baseDir}
	if s.hostHome != "" {
		roots = append(roots, s.hostHome)
	}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if resolved == absRoot || strings.HasPrefix(resolved, absRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func writeSampleErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
}
