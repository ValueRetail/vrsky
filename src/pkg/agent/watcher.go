package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// uploader is the part of Client a watcher needs.
type uploader interface {
	Upload(ctx context.Context, connectionID, directory, filename, uploadID string, body io.Reader, size int64) error
}

// dirWatcher scans one read folder and uploads each new file to every pipeline
// currently watching that folder, then moves it to processed/ or deletes it.
//
// Files already in the folder when the agent starts ARE picked up: a file
// still sitting in a read folder has not been processed, since processed files
// are moved out. A file is taken only once its size and modification time have
// been unchanged across two consecutive scans — the writing program must have
// finished with it. That is a heuristic, not a lock; see the operator docs.
type dirWatcher struct {
	name     string
	dir      DirConfig
	interval time.Duration
	client   uploader
	state    *State
	log      *slog.Logger

	mu    sync.Mutex
	conns map[string]string // connectionID → after (move | delete)

	seen map[string]fileSig // from the previous scan
}

type fileSig struct {
	size  int64
	mtime int64
}

func newDirWatcher(name string, dir DirConfig, interval time.Duration, c uploader, st *State, log *slog.Logger) *dirWatcher {
	return &dirWatcher{
		name: name, dir: dir, interval: interval, client: c, state: st,
		log: log.With("folder", name), conns: map[string]string{}, seen: map[string]fileSig{},
	}
}

// setConnections replaces the set of pipelines watching this folder.
func (w *dirWatcher) setConnections(conns map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conns = conns
}

func (w *dirWatcher) connections() ([]string, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ids := make([]string, 0, len(w.conns))
	deleteAll := len(w.conns) > 0
	for id, after := range w.conns {
		ids = append(ids, id)
		if after != agentproto.AfterDelete {
			deleteAll = false
		}
	}
	sort.Strings(ids)
	// Delete only if every watching pipeline asked for it; otherwise keep the
	// file, in processed/.
	if deleteAll {
		return ids, agentproto.AfterDelete
	}
	return ids, agentproto.AfterMove
}

func (w *dirWatcher) run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		w.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ignored reports names a watcher never takes: hidden files, the agent's own
// temp files, and anything still named as partial.
func ignored(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, partPrefix) ||
		strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".tmp")
}

func (w *dirWatcher) scan(ctx context.Context) {
	entries, err := os.ReadDir(w.dir.Path)
	if err != nil {
		w.log.Error("Cannot read folder", "path", w.dir.Path, "error", err)
		return
	}
	cur := make(map[string]fileSig, len(entries))
	for _, e := range entries {
		if e.IsDir() || ignored(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		cur[e.Name()] = sig
		if prev, ok := w.seen[e.Name()]; ok && prev == sig && time.Since(info.ModTime()) >= w.interval {
			if ctx.Err() != nil {
				return
			}
			w.take(ctx, e.Name(), sig)
		}
	}
	w.seen = cur
}

// permanent reports upload refusals that retrying cannot fix.
func permanent(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == http.StatusRequestEntityTooLarge ||
		ae.Code == agentproto.ErrInvalidFilename
}

// take uploads one stable file to every watching pipeline, then clears it.
func (w *dirWatcher) take(ctx context.Context, name string, sig fileSig) {
	conns, after := w.connections()
	if len(conns) == 0 {
		return
	}
	if err := agentproto.ValidFilename(name); err != nil {
		w.reject(name, err)
		return
	}
	path := filepath.Join(w.dir.Path, name)
	var keys []string
	for _, conn := range conns {
		key := strings.Join([]string{conn, w.name, name, strconv.FormatInt(sig.size, 10), strconv.FormatInt(sig.mtime, 10)}, "|")
		keys = append(keys, key)
		if w.state.isDone(key) {
			continue // uploaded earlier; the move or delete is what is left
		}
		uploadID, err := w.state.uploadID(key)
		if err != nil {
			w.log.Error("Cannot record upload state", "error", err)
			return
		}
		if err := w.upload(ctx, conn, name, uploadID, path, sig.size); err != nil {
			if permanent(err) {
				w.reject(name, err)
				_ = w.state.forget(keys...)
				return
			}
			w.log.Warn("Upload failed; will retry", "file", name, "connection_id", conn, "error", err)
			return
		}
		if err := w.state.markDone(key); err != nil {
			w.log.Error("Cannot record upload state", "error", err)
			return
		}
		w.log.Info("Uploaded", "file", name, "bytes", sig.size, "connection_id", conn)
	}

	if err := w.clear(name, after); err != nil {
		w.log.Warn("Uploaded but could not clear the file; will retry", "file", name, "error", err)
		return
	}
	_ = w.state.forget(keys...)
	delete(w.seen, name)
}

func (w *dirWatcher) upload(ctx context.Context, conn, name, uploadID, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err // e.g. still locked by the program writing it
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	return w.client.Upload(ctx, conn, w.name, name, uploadID, io.LimitReader(f, size), size)
}

// clear moves an uploaded file into processed/, or deletes it.
func (w *dirWatcher) clear(name, after string) error {
	path := filepath.Join(w.dir.Path, name)
	if after == agentproto.AfterDelete {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return moveInto(path, filepath.Join(w.dir.Path, "processed"))
}

// reject moves a file VRSky will never accept into rejected/, beside a note
// saying why, so it is visible to a person rather than retried forever.
func (w *dirWatcher) reject(name string, why error) {
	w.log.Error("File refused; moved to rejected/", "file", name, "reason", why)
	dest := filepath.Join(w.dir.Path, "rejected")
	if err := moveInto(filepath.Join(w.dir.Path, name), dest); err != nil {
		w.log.Error("Could not move refused file", "file", name, "error", err)
		return
	}
	_ = os.WriteFile(filepath.Join(dest, name+".reason.txt"),
		[]byte(time.Now().Format(time.RFC3339)+"  "+why.Error()+"\n"), 0o644)
	delete(w.seen, name)
}

// moveInto moves a file into dir, creating dir, and never overwrites: a name
// already there gets a timestamp suffix.
func moveInto(path, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(dest); err == nil {
		dest = fmt.Sprintf("%s.%d", dest, time.Now().UnixNano())
	}
	return renameWithRetry(path, dest)
}
