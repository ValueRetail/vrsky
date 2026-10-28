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

	"github.com/google/uuid"

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
//
// This row is the snapshot: the whole set, written at the first save after a
// start, when the changes since it pile up, and when the watcher stops. Gen
// is a new random id at each write; empty in a row from before #426 or one
// the previous version wrote. Between snapshots a busy poll writes only the
// changes, to a second row (changesState), because rewriting every name each
// time cost about 11 MB per 100 000 names every 5 s (#426).
type knownState struct {
	WatchDir string                `json:"watch_dir"`
	Gen      string                `json:"gen,omitempty"`
	Known    []string              `json:"known"`
	Files    map[string]savedStamp `json:"files,omitempty"`
}

// changesState is the second checkpoint row of the file node (node id
// changesNodeID): every name whose entry changed since the snapshot whose Gen
// is Base, as it is now. Files are done names with their stamp, Known done
// names with none, Gone names that are no longer done. It is rewritten whole
// at each save until the next snapshot, so it stays small. A changes row whose
// Base is not the snapshot's Gen is stale (a newer snapshot was written, or
// the previous version rewrote the snapshot) and is never applied. The
// previous version never reads it.
type changesState struct {
	WatchDir string                `json:"watch_dir"`
	Base     string                `json:"base"`
	Files    map[string]savedStamp `json:"files,omitempty"`
	Known    []string              `json:"known,omitempty"`
	Gone     []string              `json:"gone,omitempty"`
}

// changesSuffix turns the file node's id into the id of its changes row.
const changesSuffix = "#changes"

// maxNodeIDLen is the width of connection_node_checkpoints.node_id. A node
// id too long to take changesSuffix only ever gets snapshots.
const maxNodeIDLen = 255

// maxChanges is how many changed names the changes row holds before the next
// save writes a snapshot instead. Near the square root of twice the set size
// at 100 000 names, which keeps the bytes per busy poll lowest; a set smaller
// than twice this gets a snapshot once half of it changed.
const maxChanges = 512

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
	// inChanges means part of it came from a changes row, not the snapshot
	// (#426).
	inChanges bool
}

// touch records that name's entry in the done set changed, for the next save.
func (ac *ActiveConnection) touch(name string) {
	if ac.changed == nil {
		ac.changed = make(map[string]bool)
	}
	ac.changed[name] = true
	ac.dirty = true
}

// changesNodeID is the node id of ac's changes row, or "" if the file node's
// id is too long to make one.
func changesNodeID(ac *ActiveConnection) string {
	id := ac.NodeID + changesSuffix
	if len(id) > maxNodeIDLen {
		return ""
	}
	return id
}

// loadChanges reads ac's changes row. found is false when there is none, or
// it is for another folder.
func (s *fileConsumer) loadChanges(ctx context.Context, ac *ActiveConnection) (ch changesState, found bool, err error) {
	id := changesNodeID(ac)
	if id == "" {
		return changesState{}, false, nil
	}
	cp, err := s.checkpoints.Get(ctx, ac.TenantID, ac.ConnectionID, id)
	if err != nil {
		return changesState{}, false, err
	}
	if cp == nil || len(cp.State) == 0 {
		return changesState{}, false, nil
	}
	if err := json.Unmarshal(cp.State, &ch); err != nil {
		return changesState{}, false, fmt.Errorf("decode saved file name changes: %w", err)
	}
	if ch.WatchDir != ac.WatchDir || ch.Base == "" {
		return changesState{}, false, nil
	}
	return ch, true, nil
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

// loadSaved reads the done set saved for ac's folder: the snapshot, and the
// changes row on top of it when that row's Base is the snapshot's Gen. found
// is false when there is no checkpoint for this folder yet. Every format
// loads: names only (before #420), names with stamps, with or without a
// changes row (#426).
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
		saved.stamps[name] = f.stamp()
	}
	if st.Gen == "" {
		return saved, true, nil
	}
	ch, chFound, err := s.loadChanges(ctx, ac)
	if err != nil {
		return savedSet{}, false, err
	}
	if !chFound || ch.Base != st.Gen {
		// None yet, or stale: written on an older snapshot.
		return saved, true, nil
	}
	for name, f := range ch.Files {
		saved.names[name] = true
		saved.stamps[name] = f.stamp()
	}
	for _, name := range ch.Known {
		saved.names[name] = true
		delete(saved.stamps, name)
	}
	for _, name := range ch.Gone {
		delete(saved.names, name)
		delete(saved.stamps, name)
	}
	saved.inChanges = len(ch.Files)+len(ch.Known)+len(ch.Gone) > 0
	return saved, true, nil
}

func (f savedStamp) stamp() fileStamp {
	return fileStamp{size: f.Size, mtimeSec: f.MTime, mtimeNsec: f.MTimeNsec}
}

func toSaved(st fileStamp) savedStamp {
	return savedStamp{Size: st.size, MTime: st.mtimeSec, MTimeNsec: st.mtimeNsec}
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
	if err == nil || !requireSaved {
		// This watcher has no snapshot of its own yet: its first save writes
		// one (#426).
		ac.savedGen = ""
		ac.changed = nil
	}
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
		// A set partly read from a changes row (the last watcher did not
		// stop cleanly) is saved as a snapshot too, so the previous version
		// finds all of it after a rollback (#426).
		ac.dirty = len(known) != len(saved.names) || adopted > 0 || saved.inChanges
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
//
// Only the small changes row is read while it is still on the snapshot this
// watcher wrote last (ownsSaved): nobody else has saved since (#426).
// Anything else (no snapshot of its own yet, or the changes row is missing or
// on another snapshot) means another writer may have saved: the whole set is
// read, as before, and the next save writes a new snapshot, since changes on
// the old one would no longer be read. It reports false if the saved set
// could not be read.
func (s *fileConsumer) mergeSaved(ctx context.Context, ac *ActiveConnection, current map[string]bool, logger *slog.Logger) bool {
	if s.checkpoints == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	mine, err := s.ownsSaved(ctx, ac)
	if err != nil {
		logger.Warn("Could not re-read which files were already published", "error", err)
		return false
	}
	if mine {
		return true
	}
	saved, found, err := s.loadSaved(ctx, ac)
	if err != nil {
		logger.Warn("Could not re-read which files were already published", "error", err)
		return false
	}
	ac.savedGen = ""
	if !found {
		return true
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
				ac.touch(name)
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
		ac.touch(name)
	}
	return true
}

// ownsSaved reports whether the saved set is still this watcher's: its
// changes row is on the snapshot this watcher wrote last. Only the watcher
// that wrote a snapshot writes changes on it, and saveKnown checks this before
// each such write, so then nobody else has saved since. It reads only the
// changes row.
func (s *fileConsumer) ownsSaved(ctx context.Context, ac *ActiveConnection) (bool, error) {
	if ac.savedGen == "" {
		return false, nil
	}
	ch, found, err := s.loadChanges(ctx, ac)
	if err != nil {
		return false, err
	}
	return found && ch.Base == ac.savedGen, nil
}

// saveKnown writes ac.knownFiles to the checkpoint if it changed since the
// last successful save: only the changes since this watcher's snapshot while
// they are few, otherwise a new snapshot (#426). It runs on a context detached
// from the watcher's, so a watcher being stopped still records what it
// published; a failure keeps the set dirty for the next poll to retry.
//
// Changes are only written while the saved set is still this watcher's
// (ownsSaved). If another watcher wrote a snapshot since, changes on this
// one's would be ignored by every reader, and this watcher's own re-reads
// would take that row as proof nobody else saved. So it merges what the other
// one saved, as a poll does before publishing, and writes a snapshot instead.
// If that can't be read, the snapshot is written as it is, as before #426.
func (s *fileConsumer) saveKnown(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) {
	if s.checkpoints == nil || !ac.dirty {
		return
	}
	if ac.savedGen == "" || changesNodeID(ac) == "" ||
		len(ac.changed) > min(maxChanges, len(ac.knownFiles)/2) {
		s.saveSnapshotOrLog(ctx, ac, logger)
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
	mine, err := s.ownsSaved(readCtx, ac)
	cancel()
	if err != nil || !mine {
		if err == nil {
			s.mergeSaved(context.WithoutCancel(ctx), ac, s.currentOrKnown(ac), logger)
		}
		ac.savedGen = ""
		s.saveSnapshotOrLog(ctx, ac, logger)
		return
	}
	ch := changesState{WatchDir: ac.WatchDir, Base: ac.savedGen, Files: make(map[string]savedStamp)}
	for name := range ac.changed {
		switch st, stamped := ac.knownStamps[name]; {
		case !ac.knownFiles[name]:
			ch.Gone = append(ch.Gone, name)
		case stamped:
			ch.Files[name] = toSaved(st)
		default:
			ch.Known = append(ch.Known, name)
		}
	}
	sort.Strings(ch.Gone)
	sort.Strings(ch.Known)
	if err := s.writeState(ctx, ac, changesNodeID(ac), ch); err != nil {
		logger.Error("Could not save which files were published; retrying next poll", "error", err)
		return
	}
	ac.dirty = false
}

func (s *fileConsumer) saveSnapshotOrLog(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) {
	if err := s.saveSnapshot(ctx, ac, logger); err != nil {
		logger.Error("Could not save which files were published; retrying next poll", "error", err)
	}
}

// currentOrKnown is the names in ac's folder, or the names ac knows if the
// folder can't be read: only those can be merged then.
func (s *fileConsumer) currentOrKnown(ac *ActiveConnection) map[string]bool {
	current, err := s.listFiles(ac.WatchDir)
	if err != nil {
		return ac.knownFiles
	}
	return current
}

// saveSnapshot writes the whole done set under a new Gen, then an empty
// changes row on it. The snapshot goes first: until the changes row follows,
// the one there is on an older snapshot and is ignored, so a crash in between
// loses nothing. If only the changes row fails, the set stays dirty and the
// next save writes it. It returns the error of the snapshot write, which
// leaves everything as it was.
func (s *fileConsumer) saveSnapshot(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) error {
	names := make([]string, 0, len(ac.knownFiles))
	files := make(map[string]savedStamp, len(ac.knownStamps))
	for name := range ac.knownFiles {
		names = append(names, name)
		if st, ok := ac.knownStamps[name]; ok {
			files[name] = toSaved(st)
		}
	}
	sort.Strings(names)
	gen := uuid.NewString()
	if err := s.writeState(ctx, ac, ac.NodeID, knownState{WatchDir: ac.WatchDir, Gen: gen, Known: names, Files: files}); err != nil {
		return err
	}
	ac.savedGen = gen
	ac.changed = nil
	ac.dirty = false
	id := changesNodeID(ac)
	if id == "" {
		return nil
	}
	if err := s.writeState(ctx, ac, id, changesState{WatchDir: ac.WatchDir, Base: gen}); err != nil {
		// Readers ignore the old changes row: it is on an older snapshot.
		logger.Warn("Saved which files were published, but could not reset the list of changes; the next save does",
			"error", err)
		ac.dirty = true
	}
	return nil
}

// flushKnown writes a snapshot when the watcher stops with changes not in
// one yet, so the previous version, which reads only the snapshot, finds the
// whole set after a rollback (#426). It merges what another watcher saved
// first, as a poll does before publishing: written blind, the snapshot would
// drop a name the other one published since, and the next start would
// publish it again. If the saved set can't be read it writes nothing; the
// changes row still holds what the snapshot lacks.
func (s *fileConsumer) flushKnown(ctx context.Context, ac *ActiveConnection, logger *slog.Logger) {
	if s.checkpoints == nil || ac.baselineDeferred || (len(ac.changed) == 0 && !ac.dirty) {
		return
	}
	if !s.mergeSaved(context.WithoutCancel(ctx), ac, s.currentOrKnown(ac), logger) {
		return
	}
	if err := s.saveSnapshot(ctx, ac, logger); err != nil {
		logger.Error("Could not save the full list of published files at stop; the next start reads the list of changes",
			"error", err)
	}
}

// writeState saves state as the checkpoint row nodeID of ac's connection, on
// a context detached from ctx.
func (s *fileConsumer) writeState(ctx context.Context, ac *ActiveConnection, nodeID string, state any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
	defer cancel()
	return s.checkpoints.Save(ctx, &checkpoint.Checkpoint{
		TenantID:        ac.TenantID,
		ConnectionID:    ac.ConnectionID,
		NodeID:          nodeID,
		LastProcessedAt: time.Now().UTC(),
		State:           data,
	})
}
