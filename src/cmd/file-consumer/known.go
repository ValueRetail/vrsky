package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// The watcher's done set (ActiveConnection.knownFiles) is kept in the file
// node's checkpoint row, so a restart knows which names in the folder were
// already published and which only arrived, or were still settling, before
// the stop (#391). Before this the start took every name in the folder as
// done, and a file that landed in the last poll or two before a deploy was
// never published. See plans/issue-391-restart-files.md.

// fallbackNodeID keys the checkpoint of a file node that has no id.
const fallbackNodeID = "file-consumer"

// checkpointTimeout bounds one checkpoint read or write.
const checkpointTimeout = 5 * time.Second

// knownState is what goes into connection_node_checkpoints.state. WatchDir is
// the resolved folder the names belong to: a checkpoint for another folder
// (the path was edited) is not used.
type knownState struct {
	WatchDir string   `json:"watch_dir"`
	Known    []string `json:"known"`
}

// fileNodeID returns the id of the connection's file consumer node, the key of
// its checkpoint.
func (s *fileConsumer) fileNodeID(conn *Connection) string {
	var nodes []Node
	if err := json.Unmarshal(conn.Nodes, &nodes); err != nil {
		return fallbackNodeID
	}
	for _, node := range nodes {
		if node.Type != "consumer" || node.ID == "" {
			continue
		}
		var config struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(node.Config, &config) == nil && config.Type == "file" {
			return node.ID
		}
	}
	return fallbackNodeID
}

// loadSaved reads the names saved for ac's folder. found is false when there
// is no checkpoint for this folder yet.
func (s *fileConsumer) loadSaved(ctx context.Context, ac *ActiveConnection) (names map[string]bool, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	cp, err := s.checkpoints.Get(ctx, ac.TenantID, ac.ConnectionID, ac.NodeID)
	if err != nil {
		return nil, false, err
	}
	if cp == nil || len(cp.State) == 0 {
		return nil, false, nil
	}
	var st knownState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		return nil, false, fmt.Errorf("decode saved file names: %w", err)
	}
	if st.WatchDir != ac.WatchDir {
		return nil, false, nil
	}
	names = make(map[string]bool, len(st.Known))
	for _, name := range st.Known {
		names[name] = true
	}
	return names, true, nil
}

// loadKnown sets ac.knownFiles at start, before the connection shows as
// active (#366). On the first start (no checkpoint for this folder) every
// name in the folder is done, as before. After that only the names saved as
// done are; anything else in the folder arrived or was still settling while
// the connection was not watching, and goes through the normal stability
// check. A failed folder read returns the error and changes nothing: no
// baseline, no checkpoint read or write (#398). The start then watches with
// the baseline deferred, and the first poll whose read works calls this
// again. A failed checkpoint read falls back to the first-start rule for this
// run but does not save it: that would overwrite the saved history with
// "everything present is done", and another watcher re-reading it could then
// skip a file it should publish. The first real change saves.
func (s *fileConsumer) loadKnown(ctx context.Context, ac *ActiveConnection) error {
	current, err := s.listFiles(ac.WatchDir)
	if err != nil {
		return err
	}
	ac.knownFiles = current
	if s.checkpoints == nil {
		return nil
	}
	logger := s.logger.With("connection_id", ac.ConnectionID, "watch_dir", ac.WatchDir)
	saved, found, err := s.loadSaved(ctx, ac)
	switch {
	case err != nil:
		logger.Error("Could not read which files were already published; treating every file in the folder as done",
			"error", err)
	case !found:
		// Saved once the start is active, so a first start is recorded even
		// if nothing is ever published.
		ac.dirty = true
		logger.Info("First start for this folder: files already in it are not published", "files", len(current))
	default:
		known := make(map[string]bool, len(saved))
		for name := range current {
			if saved[name] {
				known[name] = true
			}
		}
		ac.knownFiles = known
		ac.dirty = len(known) != len(saved)
		logger.Info("Resuming: files that arrived while stopped will be published",
			"already_done", len(known), "new", len(current)-len(known))
	}
	return nil
}

// mergeSaved adds to ac.knownFiles every name in current that the checkpoint
// says is done. Another watcher for this connection may have published it
// since this one loaded the set: the old pod in a rolling deploy, or the
// previous watcher still finishing a poll after a stop and a quick start.
func (s *fileConsumer) mergeSaved(ctx context.Context, ac *ActiveConnection, current map[string]bool, logger *slog.Logger) {
	if s.checkpoints == nil {
		return
	}
	saved, found, err := s.loadSaved(ctx, ac)
	if err != nil {
		logger.Warn("Could not re-read which files were already published", "error", err)
		return
	}
	if !found {
		return
	}
	for name := range saved {
		if current[name] && !ac.knownFiles[name] {
			ac.knownFiles[name] = true
			delete(ac.pending, name)
			ac.dirty = true
		}
	}
}

// saveKnown writes ac.knownFiles to the checkpoint if it changed since the
// last successful save. It runs on a context detached from the watcher's, so
// a watcher being stopped still records what it published; a failure keeps
// the set dirty for the next poll to retry.
func (s *fileConsumer) saveKnown(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) {
	if s.checkpoints == nil || !ac.dirty {
		return
	}
	names := make([]string, 0, len(ac.knownFiles))
	for name := range ac.knownFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	state, err := json.Marshal(knownState{WatchDir: ac.WatchDir, Known: names})
	if err != nil {
		logger.Error("Could not encode the published file names", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
	defer cancel()
	if err := s.checkpoints.Save(ctx, &checkpoint.Checkpoint{
		TenantID:        ac.TenantID,
		ConnectionID:    ac.ConnectionID,
		NodeID:          ac.NodeID,
		LastProcessedAt: time.Now().UTC(),
		State:           state,
	}); err != nil {
		logger.Error("Could not save which files were published; retrying next poll", "error", err)
		return
	}
	ac.dirty = false
}
