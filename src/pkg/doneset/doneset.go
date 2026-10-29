// Package doneset keeps a polling consumer's done set, the names it already
// published and the version of each, in connection_node_checkpoints, so a
// restart does not publish everything still in the remote folder or bucket
// again (#538). The SFTP and cloud-storage consumers use it in poll mode.
//
// It is the file consumer's design (#391, #420, #426;
// src/cmd/file-consumer/known.go) for sources whose listing already carries
// each entry's size and mtime:
//
//   - The node's checkpoint row is the snapshot: every done name with the
//     stamp it was published with, under a random Gen.
//   - A second row, node id <node>#changes, holds the names changed since that
//     snapshot (done with a new stamp, or gone) and the snapshot's Gen as
//     Base. A busy poll rewrites only that small row. Past maxChanges, or at
//     the first save after a load, a save writes a new snapshot instead.
//   - A changes row whose Base is not the snapshot's Gen is stale and never
//     applied.
//
// A Set belongs to one poller goroutine and is not safe for concurrent use.
// Two pollers of one connection (the old pod in a rolling deploy, or an old
// poller finishing its poll after a quick stop and start) each have their own
// Set over the same rows; Refresh and Save merge what the other one saved.
package doneset

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// opTimeout bounds one checkpoint read or write.
const opTimeout = 5 * time.Second

// changesSuffix turns the node id into the id of its changes row.
const changesSuffix = "#changes"

// maxNodeIDLen is the width of connection_node_checkpoints.node_id. A node id
// too long to take changesSuffix only ever gets snapshots.
const maxNodeIDLen = 255

// maxChanges is how many changed names the changes row holds before the next
// save writes a snapshot instead; a set smaller than twice this gets a
// snapshot once half of it changed. Same rule as the file consumer (#426).
const maxChanges = 512

// Stamp is the version of a file or object a done name was published as: its
// size and modification time from the listing. A done name whose listing shows
// another stamp was replaced, and is published again. Stamps are only compared
// for equality.
type Stamp struct {
	Size      int64 `json:"size"`
	MTime     int64 `json:"mtime"`
	MTimeNsec int64 `json:"mtime_nsec"`
}

// StampOf returns the stamp of a listing entry.
func StampOf(size int64, mtime time.Time) Stamp {
	if mtime.IsZero() {
		return Stamp{Size: size}
	}
	return Stamp{Size: size, MTime: mtime.Unix(), MTimeNsec: int64(mtime.Nanosecond())}
}

// Key names the checkpoint rows of one consumer node.
type Key struct {
	TenantID     string
	ConnectionID string
	NodeID       string
}

// snapshotState is the snapshot row's state. Scope says where the names live
// (a remote folder, a bucket and prefix): a row for another scope is not used.
type snapshotState struct {
	Scope string           `json:"scope"`
	Gen   string           `json:"gen"`
	Files map[string]Stamp `json:"files"`
}

// changesState is the changes row's state: Files are names done (again) since
// the snapshot whose Gen is Base, with their stamp; Gone are names no longer
// done.
type changesState struct {
	Scope string           `json:"scope"`
	Base  string           `json:"base"`
	Files map[string]Stamp `json:"files,omitempty"`
	Gone  []string         `json:"gone,omitempty"`
}

// Set is one poller's done set.
type Set struct {
	store checkpoint.Store
	key   Key
	scope string

	loaded bool
	done   map[string]Stamp
	// changed holds the names whose entry changed since this Set's snapshot.
	changed map[string]bool
	// gen is the snapshot this Set wrote last; "" until it writes one, and
	// again after Refresh or Save found another writer's data.
	gen   string
	dirty bool
	// present is the last listing passed to Observe.
	present map[string]Stamp
}

// New returns an empty Set for key and scope. With a nil store nothing is
// saved or read: the Set is loaded at once and lives only in memory, as the
// consumers' done set did before #538.
func New(store checkpoint.Store, key Key, scope string) *Set {
	return &Set{
		store:  store,
		key:    key,
		scope:  scope,
		loaded: store == nil,
		done:   make(map[string]Stamp),
	}
}

// Loaded reports whether the saved set has been read. Until it has, the
// consumer must not publish: it can't tell what is already done.
func (s *Set) Loaded() bool { return s.loaded }

// Len is the number of done names.
func (s *Set) Len() int { return len(s.done) }

// Load reads the saved set. found is false on a first start: no saved set, or
// one for another scope. Then the Set starts empty and its first Save records
// it. On an error the Set is left as it was and stays unloaded.
func (s *Set) Load(ctx context.Context) (found bool, err error) {
	if s.store == nil {
		s.loaded = true
		return false, nil
	}
	saved, found, err := s.loadSaved(ctx)
	if err != nil {
		return false, err
	}
	s.loaded = true
	s.gen = ""
	s.changed = nil
	s.done = saved
	if s.done == nil {
		s.done = make(map[string]Stamp)
	}
	// A first start is saved even if nothing is published, so the next start
	// is not a first one.
	s.dirty = !found
	return found, nil
}

// Observe takes the poll's listing: name to current stamp. Done names missing
// from it are no longer done, so a file deleted and added again is published
// again.
func (s *Set) Observe(listing map[string]Stamp) {
	s.present = listing
	for name := range s.done {
		if _, ok := listing[name]; !ok {
			delete(s.done, name)
			s.touch(name)
		}
	}
}

// Done reports whether name was published with stamp st.
func (s *Set) Done(name string, st Stamp) bool {
	got, ok := s.done[name]
	return ok && got == st
}

// Mark records that name was published with stamp st.
func (s *Set) Mark(name string, st Stamp) {
	s.done[name] = st
	s.touch(name)
}

func (s *Set) touch(name string) {
	if s.changed == nil {
		s.changed = make(map[string]bool)
	}
	s.changed[name] = true
	s.dirty = true
}

// Refresh adds what another poller of the same node saved since this one
// loaded: a saved name counts only while the last listing still shows the
// stamp it was saved with. While the changes row is still on this Set's own
// snapshot nobody else saved, and only that small row is read. Call it before
// the first publish of a poll, after Observe.
func (s *Set) Refresh(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	mine, err := s.ownsSaved(ctx)
	if err != nil {
		return err
	}
	if mine {
		return nil
	}
	return s.mergeSaved(ctx)
}

// mergeSaved reads the whole saved set and merges it. The next save is a
// snapshot: changes on this Set's old snapshot would no longer be read.
func (s *Set) mergeSaved(ctx context.Context) error {
	saved, found, err := s.loadSaved(ctx)
	if err != nil {
		return err
	}
	s.gen = ""
	if !found {
		return nil
	}
	for name, theirs := range saved {
		now, ok := s.present[name]
		if !ok || now != theirs {
			// Gone, or replaced since the other poller published it.
			continue
		}
		if mine, ok := s.done[name]; ok && mine == theirs {
			continue
		}
		s.done[name] = theirs
		s.touch(name)
	}
	return nil
}

// Save writes what changed since the last successful save, on a context
// detached from ctx so a poller being stopped still records what it
// published. Only the changes are written while they are few and the saved
// set is still this Set's; otherwise a new snapshot, after merging what
// another poller saved. On an error the Set stays dirty and the next Save
// tries again.
func (s *Set) Save(ctx context.Context) error {
	if s.store == nil || !s.loaded || !s.dirty {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	// Without a changes row (node id too long) this is never true, so every
	// save is a merged snapshot.
	mine, err := s.ownsSaved(ctx)
	if err != nil {
		return err
	}
	if !mine {
		// No snapshot of its own yet, or another poller wrote one since:
		// changes on this one's would be ignored by every reader. Merge what
		// is saved first, so the snapshot keeps what another poller saved
		// since this one last read. Without a compare-and-swap a save landing
		// between that read and this write is still lost (the plan's risks).
		if err := s.mergeSaved(ctx); err != nil {
			return err
		}
		return s.snapshot(ctx)
	}
	if len(s.changed) > min(maxChanges, len(s.done)/2) {
		return s.snapshot(ctx)
	}
	ch := changesState{Scope: s.scope, Base: s.gen, Files: make(map[string]Stamp)}
	for name := range s.changed {
		if st, ok := s.done[name]; ok {
			ch.Files[name] = st
		} else {
			ch.Gone = append(ch.Gone, name)
		}
	}
	if err := s.write(ctx, s.changesID(), ch); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// snapshot writes the whole set under a new Gen, then an empty changes row on
// it. The snapshot goes first: until the changes row follows, the one there is
// on an older snapshot and is ignored, so a crash in between loses nothing.
func (s *Set) snapshot(ctx context.Context) error {
	gen := uuid.NewString()
	if err := s.write(ctx, s.key.NodeID, snapshotState{Scope: s.scope, Gen: gen, Files: s.done}); err != nil {
		return err
	}
	s.gen = gen
	s.changed = nil
	s.dirty = false
	id := s.changesID()
	if id == "" {
		return nil
	}
	if err := s.write(ctx, id, changesState{Scope: s.scope, Base: gen}); err != nil {
		// Readers ignore the old changes row: it is on an older snapshot. The
		// next Save writes a snapshot again.
		s.dirty = true
		return fmt.Errorf("saved the done set but could not reset its changes: %w", err)
	}
	return nil
}

// ownsSaved reports whether the changes row is on the snapshot this Set wrote
// last. Only the Set that wrote a snapshot writes changes on it, and Save
// checks this before each such write, so then nobody else has saved since.
func (s *Set) ownsSaved(ctx context.Context) (bool, error) {
	if s.gen == "" {
		return false, nil
	}
	ch, found, err := s.loadChanges(ctx)
	if err != nil {
		return false, err
	}
	return found && ch.Base == s.gen, nil
}

// loadSaved reads the snapshot, and the changes row on top of it when that
// row's Base is the snapshot's Gen.
func (s *Set) loadSaved(ctx context.Context) (map[string]Stamp, bool, error) {
	var st snapshotState
	found, err := s.read(ctx, s.key.NodeID, &st)
	if err != nil {
		return nil, false, fmt.Errorf("read the done set: %w", err)
	}
	if !found || st.Scope != s.scope {
		return nil, false, nil
	}
	saved := make(map[string]Stamp, len(st.Files))
	for name, f := range st.Files {
		saved[name] = f
	}
	if st.Gen == "" {
		return saved, true, nil
	}
	ch, chFound, err := s.loadChanges(ctx)
	if err != nil {
		return nil, false, err
	}
	if !chFound || ch.Base != st.Gen {
		// None yet, or stale: written on an older snapshot.
		return saved, true, nil
	}
	for name, f := range ch.Files {
		saved[name] = f
	}
	for _, name := range ch.Gone {
		delete(saved, name)
	}
	return saved, true, nil
}

// loadChanges reads the changes row. found is false when there is none, or it
// is for another scope.
func (s *Set) loadChanges(ctx context.Context) (changesState, bool, error) {
	id := s.changesID()
	if id == "" {
		return changesState{}, false, nil
	}
	var ch changesState
	found, err := s.read(ctx, id, &ch)
	if err != nil {
		return changesState{}, false, fmt.Errorf("read the done set's changes: %w", err)
	}
	if !found || ch.Scope != s.scope || ch.Base == "" {
		return changesState{}, false, nil
	}
	return ch, true, nil
}

func (s *Set) changesID() string {
	id := s.key.NodeID + changesSuffix
	if len(id) > maxNodeIDLen {
		return ""
	}
	return id
}

func (s *Set) read(ctx context.Context, nodeID string, into any) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cp, err := s.store.Get(ctx, s.key.TenantID, s.key.ConnectionID, nodeID)
	if err != nil {
		return false, err
	}
	if cp == nil || len(cp.State) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(cp.State, into); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	return true, nil
}

func (s *Set) write(ctx context.Context, nodeID string, state any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if err := s.store.Save(ctx, &checkpoint.Checkpoint{
		TenantID:        s.key.TenantID,
		ConnectionID:    s.key.ConnectionID,
		NodeID:          nodeID,
		LastProcessedAt: time.Now().UTC(),
		State:           data,
	}); err != nil {
		return fmt.Errorf("save the done set: %w", err)
	}
	return nil
}
