package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/google/uuid"
)

type UploadServer struct {
	port    string
	service *FileConsumerService
	server  *http.Server
	logger  *slog.Logger
}

func NewUploadServer(port string, service *FileConsumerService, logger *slog.Logger) *UploadServer {
	return &UploadServer{
		port:    port,
		service: service,
		logger:  logger,
	}
}

func (us *UploadServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload/", us.handleUpload)
	mux.HandleFunc("/events/", us.handleEvents)
	mux.HandleFunc("/health", us.handleHealth)
	mux.HandleFunc("/sample-data/", us.handleSampleData)

	us.server = &http.Server{
		Addr:         ":" + us.port,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		us.logger.Info("Upload HTTP server started", "port", us.port)
		if err := us.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			us.logger.Error("Upload HTTP server error", "error", err)
		}
	}()

	return nil
}

func (us *UploadServer) Stop() {
	if us.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = us.server.Shutdown(ctx)
	}
}

func (us *UploadServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (us *UploadServer) handleUpload(w http.ResponseWriter, r *http.Request) {
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

	ac := us.service.getActiveConnection(connectionID)
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
	contentType := detectContentType(filename, data)

	env := &envelope.Envelope{
		ID:            uuid.New().String(),
		TenantID:      ac.TenantID,
		IntegrationID: ac.ConnectionID,
		Payload:       data,
		PayloadSize:   int64(len(data)),
		ContentType:   contentType,
		Source:        "upload:" + filename,
		CurrentStep:   0,
		StepHistory:   []string{"file-consumer-upload"},
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]interface{}{"filename": filename},
	}

	envData, err := json.Marshal(env)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := us.service.pub.Publish(r.Context(), ac.TenantID, ac.ConnectionID, env.ID, envData); err != nil {
		us.logger.Error("Failed to publish uploaded file to JetStream", "error", err)
		http.Error(w, "Failed to process upload", http.StatusInternalServerError)
		return
	}

	// Cache last payload
	_, _ = us.service.db.Exec("UPDATE connections SET last_payload = $1 WHERE id = $2", envData, ac.ConnectionID)

	us.service.emitEvent(ac.ConnectionID, FileEvent{
		Type: "uploaded", Filename: filename, Size: int64(len(data)),
		Time: time.Now().UTC().Format(time.RFC3339),
	})

	us.logger.Info("File uploaded and published",
		"connection_id", ac.ConnectionID,
		"filename", filename,
		"size", len(data),
		"envelope_id", env.ID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"accepted","filename":"%s","envelope_id":"%s"}`, filename, env.ID)))
}

// handleEvents is an SSE endpoint that streams file activity events
func (us *UploadServer) handleEvents(w http.ResponseWriter, r *http.Request) {
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

	ac := us.service.getActiveConnection(connectionID)
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

	ch, unsub := us.service.subscribeEvents(connectionID)
	defer unsub()

	// Send initial connected event
	fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Watching %s\"}\n\n", ac.WatchDir)
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

// handleSampleData returns a small preview of the most recently-modified file
// in the watch directory, used by the UI's filter/converter preview before a
// pipeline is deployed. Phase 1H (#73).
//
// Request:  POST {"path":"/dir"}   or   GET ?path=/dir
// Response: {"ok":true,"data":<parsed JSON | {text:"..."}>,"filename":"…"}
//           {"ok":false,"error":"…"}
//
// Path validation: the directory must be under the configured BaseDir or
// under HOST_HOME (same allowlist as the file-producer's file manager).
func (us *UploadServer) handleSampleData(w http.ResponseWriter, r *http.Request) {
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
	if !us.isSamplePathAllowed(dirPath) {
		us.logger.Warn("Sample data request blocked", "path", dirPath)
		writeSampleErr(w, "path not allowed")
		return
	}

	filename, payload, err := us.pickSampleFile(dirPath)
	if err != nil {
		writeSampleErr(w, err.Error())
		return
	}

	resp := map[string]interface{}{"ok": true, "filename": filename}
	// Prefer parsed JSON when the content looks like JSON; fall back to a
	// plain text envelope so CSV/XML/log files still render in the preview.
	ct := detectContentType(filename, payload)
	if ct == "application/json" {
		var parsed interface{}
		if err := json.Unmarshal(payload, &parsed); err == nil {
			resp["data"] = parsed
		} else {
			resp["data"] = map[string]string{"text": string(payload)}
		}
	} else {
		resp["data"] = map[string]string{"text": string(payload), "content_type": ct}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// pickSampleFile lists dir, drops hidden entries and sub-directories, sorts
// by modification time (most recent first) and reads up to 256KB from the
// winner. Returns the filename + bytes.
func (us *UploadServer) pickSampleFile(dir string) (string, []byte, error) {
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
func (us *UploadServer) isSamplePathAllowed(path string) bool {
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
	roots := []string{us.service.config.BaseDir}
	if home := os.Getenv("HOST_HOME"); home != "" {
		roots = append(roots, home)
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
