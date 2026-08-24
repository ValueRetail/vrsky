package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// fileProducer writes pipeline envelopes to the local filesystem. It is a
// connector built on the SDK: the runner owns NATS/JetStream, the durable
// subscription, the health server, signal handling and graceful shutdown;
// this type implements only Configure (set up output dir + allowed roots +
// the /files management API) and Deliver (write one envelope).
type fileProducer struct {
	sdk.BaseProducer

	db     *sql.DB
	logger *slog.Logger
	nc     *nats.Conn
	cmdSub *nats.Subscription

	defaultOutputDir string
	allowedRoots     []string

	// Cache of per-connection producer configs (a connection may have several
	// file-producer nodes).
	configCache     map[string][]*ConnectionConfig
	configCacheMu   sync.RWMutex
	configCacheTTL  time.Duration
	configCacheTime map[string]time.Time
}

// evictConfigCache drops a connection's cached config so the next message
// re-reads it from the DB. Called when a start/stop command for the connection
// arrives (i.e. a redeploy), so config edits take effect immediately instead of
// after the cache TTL (#141).
func (p *fileProducer) evictConfigCache(connectionID string) {
	p.configCacheMu.Lock()
	delete(p.configCache, connectionID)
	delete(p.configCacheTime, connectionID)
	p.configCacheMu.Unlock()
}

// handleConnectionCommand evicts the cache for the connection named in a
// start/stop command.
func (p *fileProducer) handleConnectionCommand(msg *nats.Msg) {
	var cmd struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil || cmd.ConnectionID == "" {
		return
	}
	p.evictConfigCache(cmd.ConnectionID)
	p.logger.Info("Evicted producer config cache on connection command", "connection_id", cmd.ConnectionID)
}

// Stop unsubscribes the command listener (the SDK runner closes NATS/DB).
func (p *fileProducer) Stop(ctx context.Context) error {
	if p.cmdSub != nil {
		_ = p.cmdSub.Unsubscribe()
	}
	return nil
}

// ConnectionConfig holds the file output configuration for one producer node.
type ConnectionConfig struct {
	ID             string
	TenantID       string
	OutputPath     string
	FilePattern    string
	PredecessorID  string
	PredIsConsumer bool
	// FolderName groups a connection's output under a per-connection subfolder.
	FolderName string
}

// errPathNotAllowed marks a write whose target is outside any mounted root —
// a configuration error that retrying can't fix (treated as Permanent).
var errPathNotAllowed = errors.New("output path not under a mounted directory")

func main() {
	if err := sdk.RunProducer(context.Background(), "file-producer", &fileProducer{}); err != nil {
		slog.Error("file-producer exited", "error", err)
		os.Exit(1)
	}
}

// Configure wires the producer's dependencies and output configuration. Called
// once by the runner before the subscription starts.
func (p *fileProducer) Configure(ctx context.Context, res *sdk.Resources) error {
	if res.DB == nil {
		return errors.New("file-producer requires DATABASE_URL (per-connection config lives in the connections table)")
	}
	p.db = res.DB
	p.logger = res.Logger
	p.configCache = make(map[string][]*ConnectionConfig)
	p.configCacheTime = make(map[string]time.Time)
	if p.configCacheTTL == 0 {
		p.configCacheTTL = 5 * time.Minute
	}

	p.defaultOutputDir = expandHomePath(getEnv("FILE_OUTPUT_DIR", "/tmp/file-output"))
	if err := os.MkdirAll(p.defaultOutputDir, 0o755); err != nil {
		return fmt.Errorf("create default output directory %q: %w", p.defaultOutputDir, err)
	}

	// A written file may only live under the default output dir or the mounted
	// host home — anything else would land in the container's throwaway FS.
	p.allowedRoots = []string{p.defaultOutputDir}
	if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
		p.allowedRoots = append(p.allowedRoots, hostHome)
	}

	// Register the file-management API (/files) on the SDK's auxiliary HTTP
	// server (served on FILE_PRODUCER_HTTP_PORT). This is the SDK's
	// custom-handler hook — the management API stays on this binary without
	// the SDK needing to know about it.
	allowedOrigin := getEnv("FILE_PRODUCER_ALLOWED_ORIGIN", "http://localhost:5173")
	authToken := os.Getenv("FILE_PRODUCER_AUTH_TOKEN")
	p.RegisterHTTPHandler("/files", filesHandler(p.allowedRoots, authToken, allowedOrigin, p.logger))

	// Subscribe to connection start/stop commands so a redeploy evicts this
	// connection's cached config immediately (#141). nil in some tests/harness.
	p.nc = res.NATS
	if p.nc != nil {
		sub, err := p.nc.Subscribe("vrsky.commands.*.connection.*", p.handleConnectionCommand)
		if err != nil {
			return fmt.Errorf("subscribe to connection commands: %w", err)
		}
		p.cmdSub = sub
	}

	p.logger.Info("file-producer configured", "output_dir", p.defaultOutputDir, "allowed_roots", p.allowedRoots)
	return nil
}

// Deliver writes one envelope to every matching file-producer node configured
// for its connection. Transient write failures return sdk.Retriable (the SDK
// NAKs → retries → DLQs); a path-not-allowed config error is logged and
// dropped (sdk.Permanent semantics — retrying can't help).
func (p *fileProducer) Deliver(ctx context.Context, env *envelope.Envelope) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		return sdk.Permanent(fmt.Errorf("envelope %s has no integration_id; cannot route to a file config", env.ID))
	}

	configs, err := p.getConnectionConfigs(ctx, connectionID)
	if err != nil {
		// DB hiccup — retry.
		return sdk.Retriable(fmt.Errorf("get connection config: %w", err))
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	var transient error
	for _, config := range p.eligibleConfigs(configs, lastProcessedBy) {
		outputPath := config.OutputPath
		if outputPath == "" {
			outputPath = p.defaultOutputDir
		}
		if config.FolderName != "" {
			outputPath = filepath.Join(outputPath, config.FolderName)
		}

		if werr := p.writeFile(env, outputPath, config.FilePattern, nil); werr != nil {
			if errors.Is(werr, errPathNotAllowed) {
				// Misconfiguration — don't burn the retry budget on it.
				p.logger.Error("dropping: output path not allowed", "error", werr, "envelope_id", env.ID, "path", outputPath)
				continue
			}
			p.logger.Error("failed to write file", "error", werr, "envelope_id", env.ID, "path", outputPath)
			transient = werr
			continue
		}
		p.logger.Info("file written successfully",
			"envelope_id", env.ID, "connection_id", connectionID, "path", outputPath, "size", len(env.Payload))
	}

	if transient != nil {
		return sdk.Retriable(transient)
	}
	return nil
}

// getConnectionConfigs retrieves ALL file producer configs for a connection
// (with a short cache). // lint:tenant-ok — connection lookup by PK; tenant scoping is enforced upstream when the pipeline is deployed.
func (p *fileProducer) getConnectionConfigs(ctx context.Context, connectionID string) ([]*ConnectionConfig, error) {
	p.configCacheMu.RLock()
	if configs, ok := p.configCache[connectionID]; ok {
		if time.Since(p.configCacheTime[connectionID]) < p.configCacheTTL {
			p.configCacheMu.RUnlock()
			return configs, nil
		}
	}
	p.configCacheMu.RUnlock()

	var nodesJSON, edgesJSON []byte
	var connName string
	err := p.db.QueryRowContext(ctx, `SELECT name, nodes, edges FROM connections WHERE id = $1`, connectionID).Scan(&connName, &nodesJSON, &edgesJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			// Unknown connection — not ours to write. Cache empty so we don't
			// re-query (and don't write a file) for every message of it.
			p.cacheConfigs(connectionID, nil)
			return nil, nil
		}
		return nil, fmt.Errorf("query connection config: %w", err)
	}

	var nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		// Unparseable nodes — can't identify a file output; skip rather than
		// dump every message into the default dir.
		p.cacheConfigs(connectionID, nil)
		return nil, nil
	}

	var edges []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if edgesJSON != nil {
		_ = json.Unmarshal(edgesJSON, &edges)
	}

	var configs []*ConnectionConfig
	for _, node := range nodes {
		if node.Type != "producer" {
			continue
		}
		var nodeConfig struct {
			Type string `json:"type"`
			File struct {
				Path        string `json:"path"`
				FilePattern string `json:"file_pattern"`
			} `json:"file"`
		}
		if err := json.Unmarshal(node.Config, &nodeConfig); err != nil {
			continue
		}
		if nodeConfig.Type != "file" && nodeConfig.File.Path == "" {
			continue
		}

		var predID string
		var predIsConsumer bool
		for _, e := range edges {
			if e.Target == node.ID {
				predID = e.Source
				for _, n := range nodes {
					if n.ID == predID && n.Type == "consumer" {
						predIsConsumer = true
						break
					}
				}
				break
			}
		}

		path := nodeConfig.File.Path
		if path == "" {
			path = p.defaultOutputDir
		}
		folderName := sanitizeForFilename(connName)
		if folderName == "" {
			folderName = connectionID
		}

		configs = append(configs, &ConnectionConfig{
			ID:             connectionID,
			OutputPath:     expandHomePath(path),
			FilePattern:    nodeConfig.File.FilePattern,
			PredecessorID:  predID,
			PredIsConsumer: predIsConsumer,
			FolderName:     folderName,
		})
	}

	// No file-producer node on this connection → nothing for us to write. (This
	// is the common case for non-file pipelines, e.g. webhook→http; the
	// file-producer subscribes to ALL pipeline data, so it must no-op here
	// rather than write a file per message to the default dir.)
	p.cacheConfigs(connectionID, configs)
	return configs, nil
}

func (p *fileProducer) cacheConfigs(connectionID string, configs []*ConnectionConfig) {
	p.configCacheMu.Lock()
	defer p.configCacheMu.Unlock()
	p.configCache[connectionID] = configs
	p.configCacheTime[connectionID] = time.Now()
}

// writeFile writes the envelope payload to a file under outputPath.
// DeliverStream writes a large, offloaded payload straight from the pipeline's
// object stream to disk, so a multi-GB file is written with a bounded buffer
// instead of being held in memory (ADR 0001).
//
// A stream reads once, so writing the same payload to several configured output
// directories is declined (sdk.ErrStreamUnsupported) and the SDK falls back to
// buffered delivery — behaviour identical to before.
func (p *fileProducer) DeliverStream(ctx context.Context, env *envelope.Envelope, body io.Reader) error {
	connectionID := env.IntegrationID
	if connectionID == "" {
		return sdk.Permanent(fmt.Errorf("envelope %s has no integration_id; cannot route to a file config", env.ID))
	}
	configs, err := p.getConnectionConfigs(ctx, connectionID)
	if err != nil {
		return sdk.Retriable(fmt.Errorf("get connection config: %w", err))
	}

	lastProcessedBy := ""
	if env.Metadata != nil {
		if v, ok := env.Metadata["_last_processed_by"].(string); ok {
			lastProcessedBy = v
		}
	}

	eligible := p.eligibleConfigs(configs, lastProcessedBy)
	if len(eligible) == 0 {
		return nil
	}
	if len(eligible) > 1 {
		return sdk.ErrStreamUnsupported
	}

	config := eligible[0]
	outputPath := config.OutputPath
	if outputPath == "" {
		outputPath = p.defaultOutputDir
	}
	if config.FolderName != "" {
		outputPath = filepath.Join(outputPath, config.FolderName)
	}

	if werr := p.writeFile(env, outputPath, config.FilePattern, body); werr != nil {
		if errors.Is(werr, errPathNotAllowed) {
			// Misconfiguration — retrying can't fix it.
			p.logger.Error("dropping: output path not allowed", "error", werr, "envelope_id", env.ID, "path", outputPath)
			return nil
		}
		return sdk.Retriable(werr)
	}
	p.logger.Info("file streamed successfully",
		"envelope_id", env.ID, "connection_id", connectionID, "path", outputPath, "size", env.PayloadSize)
	return nil
}

// eligibleConfigs drops the output configs whose predecessor predicate doesn't
// match this envelope. Shared by the buffered and streaming paths.
func (p *fileProducer) eligibleConfigs(configs []*ConnectionConfig, lastProcessedBy string) []*ConnectionConfig {
	out := make([]*ConnectionConfig, 0, len(configs))
	for _, config := range configs {
		if config.PredIsConsumer && lastProcessedBy != "" {
			continue
		}
		if !config.PredIsConsumer && config.PredecessorID != "" && lastProcessedBy != config.PredecessorID {
			continue
		}
		out = append(out, config)
	}
	return out
}

// writeFile writes the envelope to disk. When body is non-nil the contents are
// streamed from it and env.Payload is ignored.
func (p *fileProducer) writeFile(env *envelope.Envelope, outputPath, filePattern string, body io.Reader) error {
	// Refuse to write outside a mounted root (would silently vanish into the
	// container FS — looks like success in the logs otherwise).
	if len(p.allowedRoots) > 0 && !isPathAllowed(outputPath, p.allowedRoots) {
		return fmt.Errorf("%w: %q not under %v — check the output directory in the connection config",
			errPathNotAllowed, outputPath, p.allowedRoots)
	}

	if err := mkdirAllAndChown(outputPath); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	filename := p.generateFilename(env, filePattern)
	fullPath := filepath.Join(outputPath, filename)

	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	absOutputPath, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if !strings.HasPrefix(absPath, absOutputPath) {
		return fmt.Errorf("path traversal detected")
	}

	size := int64(len(env.Payload))
	if body != nil {
		size = env.PayloadSize
		if err := writeStream(absPath, body); err != nil {
			return err
		}
	} else if err := os.WriteFile(absPath, env.Payload, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	chownToHostUser(absPath)
	chownToHostUser(absOutputPath)

	p.logger.Debug("wrote file", "path", absPath, "size", size)
	return nil
}

// writeStream copies body to path. A failed copy removes the partial file: a
// truncated file left on disk looks complete to whatever watches that directory,
// and the redelivery will write it again anyway.
func writeStream(path string, body io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close file: %w", err)
	}
	return nil
}

// generateFilename creates a filename from the envelope and pattern.
func (p *fileProducer) generateFilename(env *envelope.Envelope, pattern string) string {
	if pattern == "" {
		ext := deriveExtension(env.ContentType)
		if env.Metadata != nil {
			if fn, ok := env.Metadata["filename"].(string); ok && fn != "" {
				if _, converted := env.Metadata["_converted"]; converted {
					baseName := fn
					if dotIdx := strings.LastIndex(fn, "."); dotIdx >= 0 {
						baseName = fn[:dotIdx]
					}
					return sanitizeForFilename(baseName + "." + ext)
				}
				return sanitizeForFilename(fn)
			}
		}
		return fmt.Sprintf("%s.%s", env.ID, ext)
	}

	filename := pattern
	filename = strings.ReplaceAll(filename, "{id}", env.ID)
	filename = strings.ReplaceAll(filename, "{timestamp}", env.CreatedAt.Format("20060102-150405"))
	filename = strings.ReplaceAll(filename, "{extension}", deriveExtension(env.ContentType))
	filename = strings.ReplaceAll(filename, "{source}", sanitizeForFilename(env.Source))
	return filename
}

// deriveExtension maps content type to file extension.
func deriveExtension(contentType string) string {
	switch {
	case strings.Contains(contentType, "application/json"):
		return "json"
	case strings.Contains(contentType, "text/plain"):
		return "txt"
	case strings.Contains(contentType, "text/csv"):
		return "csv"
	case strings.Contains(contentType, "application/xml"), strings.Contains(contentType, "text/xml"):
		return "xml"
	case strings.Contains(contentType, "application/yaml"), strings.Contains(contentType, "text/yaml"):
		return "yaml"
	case strings.Contains(contentType, "application/x-ndjson"):
		return "ndjson"
	case strings.Contains(contentType, "text/html"):
		return "html"
	case strings.Contains(contentType, "text/tab-separated-values"):
		return "tsv"
	default:
		return "bin"
	}
}

// --- package-level helpers (unchanged across the SDK refactor) ---

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func expandHomePath(path string) string {
	resolveHome := func() string {
		if dir := os.Getenv("FILE_OUTPUT_DIR"); dir != "" {
			return dir
		}
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return ""
	}
	if path == "~" {
		if home := resolveHome(); home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home := resolveHome(); home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func sanitizeForFilename(s string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(s)
}

// --- File management HTTP API (served on the SDK auxiliary HTTP port) ---

// filesHandler returns the /files handler: CORS-restricted to the UI origin
// and (optionally) bearer-token gated, dispatching GET (list) / DELETE.
func filesHandler(allowedRoots []string, authToken, allowedOrigin string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Origin")
		if r.Header.Get("Origin") == allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !authorizedFileRequest(r, authToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleListFiles(w, r, allowedRoots, logger)
		case http.MethodDelete:
			handleDeleteFiles(w, r, allowedRoots, logger)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// authorizedFileRequest reports whether a /files request is permitted. When no
// token is configured the endpoint stays open (local-dev default); when set,
// the request must present a matching bearer token.
func authorizedFileRequest(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(token)) == 1
}

func isPathAllowed(path string, allowedRoots []string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// Resolve BOTH sides. Comparing a symlink-resolved candidate against an
	// unresolved root fails whenever the root contains a symlinked component
	// (/var -> /private/var on macOS, or a mount alias such as /data ->
	// /mnt/data), which silently rejected every legitimate write. Resolving both
	// keeps the escape check intact — a real traversal still resolves outside the
	// root — while making containment correct.
	resolved := resolveExisting(absPath)
	for _, root := range allowedRoots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		resolvedRoot := resolveExisting(absRoot)
		// The separator matters: without it "/data-evil" would pass as "/data".
		if resolved == resolvedRoot || strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks in path, tolerating a path that does not
// exist yet — the normal case here, since the output directory is checked before
// it is created. It resolves the deepest existing ancestor and re-attaches the
// remainder, so a not-yet-created directory under a symlinked root still
// compares correctly against a resolved root.
func resolveExisting(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	parent := filepath.Dir(clean)
	if parent == clean {
		return clean // reached the root; nothing further to resolve
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(clean))
}

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func handleListFiles(w http.ResponseWriter, r *http.Request, allowedRoots []string, logger *slog.Logger) {
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}
	if !isPathAllowed(dirPath, allowedRoots) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path not allowed"})
		return
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"files": []fileEntry{}, "path": dirPath})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	files := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:    e.Name(),
			Path:    filepath.Join(dirPath, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"files": files, "path": dirPath})
}

func handleDeleteFiles(w http.ResponseWriter, r *http.Request, allowedRoots []string, logger *slog.Logger) {
	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter required"})
		return
	}
	if !isPathAllowed(targetPath, allowedRoots) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "path not allowed"})
		return
	}
	absTarget, _ := filepath.Abs(targetPath)
	for _, root := range allowedRoots {
		absRoot, _ := filepath.Abs(root)
		if absTarget == absRoot {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot delete root output directory"})
			return
		}
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var count int
	if info.IsDir() {
		_ = filepath.WalkDir(targetPath, func(_ string, d fs.DirEntry, _ error) error {
			if d != nil && !d.IsDir() {
				count++
			}
			return nil
		})
		err = os.RemoveAll(targetPath)
	} else {
		count = 1
		err = os.Remove(targetPath)
	}
	if err != nil {
		logger.Error("Failed to delete", "path", targetPath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	logger.Info("Deleted path", "path", targetPath, "files", count)
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": targetPath, "files": count})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// chownToHostUser changes ownership to the host user (FILE_OWNER_UID/GID).
// Best-effort: silently does nothing if unset or chown fails.
func chownToHostUser(path string) {
	uid, gid := getHostOwner()
	if uid < 0 {
		return
	}
	_ = os.Chown(path, uid, gid)
}

// mkdirAllAndChown creates path + missing parents, chowning only the dirs it
// actually creates (pre-existing dirs are never touched).
func mkdirAllAndChown(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var toChown []string
	p := absPath
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		toChown = append(toChown, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return err
	}
	for _, d := range toChown {
		chownToHostUser(d)
	}
	return nil
}

func getHostOwner() (int, int) {
	uidStr := os.Getenv("FILE_OWNER_UID")
	gidStr := os.Getenv("FILE_OWNER_GID")
	if uidStr != "" && gidStr != "" {
		uid, err1 := strconv.Atoi(uidStr)
		gid, err2 := strconv.Atoi(gidStr)
		if err1 == nil && err2 == nil {
			if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
				if info, err := os.Stat(hostHome); err == nil {
					if st, ok := info.Sys().(*syscall.Stat_t); ok {
						if int(st.Uid) != uid {
							return int(st.Uid), int(st.Gid)
						}
					}
				}
			}
			return uid, gid
		}
	}
	if hostHome := os.Getenv("HOST_HOME"); hostHome != "" {
		if info, err := os.Stat(hostHome); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				return int(st.Uid), int(st.Gid)
			}
		}
	}
	return -1, -1
}
