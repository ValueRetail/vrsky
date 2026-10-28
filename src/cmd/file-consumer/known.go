package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// (the path was edited) is not used. Known lists every done name; Files holds
// the stamp each was published with (#420). Known keeps every name so a
// previous version, which reads only Known, sees the same done set after a
// rollback or as the old pod of a rolling deploy. A checkpoint from before
// #420 has no Files: its names have no stamp until loadKnown adopts one.
type knownState struct {
	WatchDir string                `json:"watch_dir"`
	Known    []string              `json:"known"`
	Files    map[string]savedStamp `json:"files,omitempty"`
}

// savedStamp is a fileStamp as stored in the checkpoint.
type savedStamp struct {
	Size      int64 `json:"size"`
	MTime     int64 `json:"mtime"`
	MTimeNsec int64 `json:"mtime_nsec"`
}

// fileStamp is what the done set remembers of a published file: its size and
// mtime when it was published. A known name whose stamp differs is a new file
// under the same name, overwritten or renamed over the old one (#420). The
// mtime is kept as seconds and nanoseconds so no year overflows UnixNano.
// Stamps are only compared for equality, never against the pod's clock.
type fileStamp struct {
	size      int64
	mtimeSec  int64
	mtimeNsec int64
}

func stampOf(info os.FileInfo) fileStamp {
	mt := info.ModTime()
	return fileStamp{size: info.Size(), mtimeSec: mt.Unix(), mtimeNsec: int64(mt.Nanosecond())}
}

// statStamp stats name in dir; ok is false if it can't.
func statStamp(dir, name string) (fileStamp, bool) {
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return fileStamp{}, false
	}
	return stampOf(info), true
}

// stampAll returns the current stamp of each name in dir. A name that can't
// be stat'ed gets none; the next poll that can stat it adopts one.
func stampAll(dir string, names map[string]bool) map[string]fileStamp {
	stamps := make(map[string]fileStamp, len(names))
	for name := range names {
		if st, ok := statStamp(dir, name); ok {
			stamps[name] = st
		}
	}
	return stamps
}

// savedSet is a decoded checkpoint: the done names and, for those saved
// since #420, their stamps.
type savedSet struct {
	names  map[string]bool
	stamps map[string]fileStamp
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

// loadSaved reads the done set saved for ac's folder. found is false when
// there is no checkpoint for this folder yet. Both formats load: names only
// (before #420), and names with stamps.
func (s *fileConsumer) loadSaved(ctx context.Context, ac *ActiveConnection) (saved savedSet, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	cp, err := s.checkpoints.Get(ctx, ac.TenantID, ac.ConnectionID, ac.NodeID)
	if err != nil {
		return savedSet{}, false, err
	}
	if cp == nil || len(cp.State) == 0 {
		return savedSet{}, false, nil
	}
	var st knownState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		return savedSet{}, false, fmt.Errorf("decode saved file names: %w", err)
	}
	if st.WatchDir != ac.WatchDir {
		return savedSet{}, false, nil
	}
	saved = savedSet{
		names:  make(map[string]bool, len(st.Known)),
		stamps: make(map[string]fileStamp, len(st.Files)),
	}
	for _, name := range st.Known {
		saved.names[name] = true
	}
	for name, f := range st.Files {
		saved.names[name] = true
		saved.stamps[name] = fileStamp{size: f.Size, mtimeSec: f.MTime, mtimeNsec: f.MTimeNsec}
	}
	return saved, true, nil
}

// errCheckpointRead marks a loadKnown that could read the folder but not the
// saved set, when the caller asked for the saved set (#409).
var errCheckpointRead = errors.New("could not read which files were already published")

// loadKnown sets ac.knownFiles at start, before the connection shows as
// active (#366). On the first start (no checkpoint for this folder) every
// name in the folder is done, as before. After that only the names saved as
// done are; anything else in the folder arrived or was still settling while
// the connection was not watching, and goes through the normal stability
// check. Each done name gets a stamp (#420): the saved one, so a file
// replaced while stopped is published; or, on a first start and for a name
// saved without one (a checkpoint from before #420), the file's stamp now,
// which publishes nothing. A failed folder read returns the error and changes nothing: no
// baseline, no checkpoint read or write (#398). The start then watches with
// the baseline deferred, and the first poll whose read works calls this
// again.
//
// A failed checkpoint read depends on requireSaved. Without it (the start)
// it falls back to the first-start rule for this run but does not save it:
// that would overwrite the saved history with "everything present is done",
// and another watcher re-reading it could then skip a file it should
// publish. The first real change saves. With it (a deferred baseline) it
// returns an error wrapping errCheckpointRead and changes nothing, so the
// baseline stays deferred and the next poll tries again: falling back there
// would count every file that landed during the outage as done (#409).
func (s *fileConsumer) loadKnown(ctx context.Context, ac *ActiveConnection, requireSaved bool) error {
	current, err := s.listFiles(ac.WatchDir)
	if err != nil {
		return err
	}
	if s.checkpoints == nil {
		ac.knownFiles = current
		ac.knownStamps = stampAll(ac.WatchDir, current)
		return nil
	}
	logger := s.logger.With("connection_id", ac.ConnectionID, "watch_dir", ac.WatchDir)
	saved, found, err := s.loadSaved(ctx, ac)
	switch {
	case err != nil && requireSaved:
		return fmt.Errorf("%w: %w", errCheckpointRead, err)
	case err != nil:
		ac.knownFiles = current
		ac.knownStamps = stampAll(ac.WatchDir, current)
		logger.Error("Could not read which files were already published; treating every file in the folder as done",
			"error", err)
	case !found:
		// Saved once the start is active, so a first start is recorded even
		// if nothing is ever published.
		ac.knownFiles = current
		ac.knownStamps = stampAll(ac.WatchDir, current)
		ac.dirty = true
		logger.Info("First start for this folder: files already in it are not published", "files", len(current))
	default:
		known := make(map[string]bool, len(saved.names))
		stamps := make(map[string]fileStamp, len(saved.names))
		adopted := 0
		for name := range current {
			if !saved.names[name] {
				continue
			}
			known[name] = true
			if st, ok := saved.stamps[name]; ok {
				// A file replaced while stopped no longer has it, and is
				// published by the poll (#420).
				stamps[name] = st
			} else if st, ok := statStamp(ac.WatchDir, name); ok {
				// Saved before #420, with no stamp: take it as it is now.
				stamps[name] = st
				adopted++
			}
		}
		ac.knownFiles = known
		ac.knownStamps = stamps
		// Adopted stamps are saved at once. Left for the next publish, a
		// quiet folder kept a name-only row, and a file replaced while
		// stopped was adopted again at each start and never published.
		// Once a row has stamps nothing is adopted, so this is one save.
		ac.dirty = len(known) != len(saved.names) || adopted > 0
		logger.Info("Resuming: files that arrived or changed while stopped will be published",
			"already_done", len(known), "new", len(current)-len(known), "stamped_now", adopted)
	}
	return nil
}

// mergeSaved adds to ac.knownFiles every name in current that the checkpoint
// says is done. Another watcher for this connection may have published it
// since this one loaded the set: the old pod in a rolling deploy, or the
// previous watcher still finishing a poll after a stop and a quick start.
// A saved stamp counts only while the file still has it; otherwise this
// watcher sees a newer version than the one published and goes on (#420). A
// name saved without a stamp (by a pod from before #420) counts only for a
// name this watcher does not know yet, as it did before.
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
	for name := range saved.names {
		if !current[name] {
			continue
		}
		theirs, stamped := saved.stamps[name]
		if !stamped {
			if !ac.knownFiles[name] {
				ac.knownFiles[name] = true
				delete(ac.pending, name)
				ac.dirty = true
			}
			continue
		}
		if mine, ok := ac.knownStamps[name]; ok && ac.knownFiles[name] && mine == theirs {
			continue
		}
		if now, ok := statStamp(ac.WatchDir, name); !ok || now != theirs {
			continue
		}
		ac.knownFiles[name] = true
		ac.knownStamps[name] = theirs
		delete(ac.pending, name)
		ac.dirty = true
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
	files := make(map[string]savedStamp, len(ac.knownStamps))
	for name := range ac.knownFiles {
		names = append(names, name)
		if st, ok := ac.knownStamps[name]; ok {
			files[name] = savedStamp{Size: st.size, MTime: st.mtimeSec, MTimeNsec: st.mtimeNsec}
		}
	}
	sort.Strings(names)
	state, err := json.Marshal(knownState{WatchDir: ac.WatchDir, Known: names, Files: files})
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
