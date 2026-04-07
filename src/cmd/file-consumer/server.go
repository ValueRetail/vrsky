package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

	topic := fmt.Sprintf("vrsky.data.%s.pipeline.%s", ac.TenantID, ac.ConnectionID)
	if err := us.service.nc.Publish(topic, envData); err != nil {
		us.logger.Error("Failed to publish uploaded file", "error", err)
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
