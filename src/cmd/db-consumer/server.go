package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type Server struct {
	port    string
	service *DBConsumerService
	server  *http.Server
	logger  *slog.Logger
}

func NewServer(port string, service *DBConsumerService, logger *slog.Logger) *Server {
	return &Server{port: port, service: service, logger: logger}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/events/", s.handleEvents)
	mux.HandleFunc("/test-connection/", s.handleTestConnection)
	mux.HandleFunc("/sample-data/", s.handleSampleData)
	mux.HandleFunc("/health", s.handleHealth)

	s.server = &http.Server{
		Addr:         ":" + s.port,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		s.logger.Info("DB Consumer HTTP server started", "port", s.port)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) Stop() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleTestConnection lets the UI verify DB credentials before deploying
func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tenant-ID")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var cfg SourceDBConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database, cfg.SSLMode)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
		_, _ = w.Write(resp)
		return
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
		_, _ = w.Write(resp)
		return
	}

	// If a table was specified, check it exists
	tables := []string{}
	if cfg.Table != "" {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = $1 AND table_schema = 'public'", cfg.Table).Scan(&count)
		if err == nil && count > 0 {
			tables = append(tables, cfg.Table)
		}
	}

	// List available tables
	if len(tables) == 0 {
		rows, err := db.Query("SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name LIMIT 50")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var t string
				if rows.Scan(&t) == nil {
					tables = append(tables, t)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]interface{}{"ok": true, "tables": tables})
	_, _ = w.Write(resp)
}

// handleSampleData returns a few rows from the configured source for preview
func (s *Server) handleSampleData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		Database string `json:"database"`
		SSLMode  string `json:"sslmode"`
		Table    string `json:"table"`
		Query    string `json:"query"`
		Limit    int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = 5432
	}
	if req.SSLMode == "" {
		req.SSLMode = "disable"
	}
	if req.Limit <= 0 || req.Limit > 10 {
		req.Limit = 3
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		req.Host, req.Port, req.User, req.Password, req.Database, req.SSLMode)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
		_, _ = w.Write(resp)
		return
	}
	defer db.Close()

	query := req.Query
	if query == "" && req.Table != "" {
		query = fmt.Sprintf("SELECT * FROM %q LIMIT %d", req.Table, req.Limit)
	}
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "No table or query specified"})
		_, _ = w.Write(resp)
		return
	}

	rows, err := db.Query(query)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
		_, _ = w.Write(resp)
		return
	}
	defer rows.Close()

	columns, _ := rows.Columns()
	var result []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]interface{})
		for i, col := range columns {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		result = append(result, row)
	}

	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]interface{}{"ok": true, "rows": result})
	_, _ = w.Write(resp)
}

// handleEvents is an SSE endpoint for streaming DB consumer activity
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.service.subscribeEvents(connectionID)
	defer unsub()

	fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"Listening for DB events\"}\n\n")
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
