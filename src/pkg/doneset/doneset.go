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
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// ErrUndecodable marks an error from reading a checkpoint row that is there
// but can't be decoded. It is not a store error: no later read will work, so
// Load treats the row as not saved and the next Save replaces it (#617).
var ErrUndecodable = errors.New("could not decode the saved done set")

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

// verifyEvery and verifyAfter bound how long a Set can keep writing changes on
// a snapshot nobody reads (#723). ownsSaved looks only at the changes row, and
// that can say "mine" after another poller's snapshot: its changes reset landed
// between this Set's read and write and was overwritten, or its reset failed.
// So after verifyEvery changes-only saves, or verifyAfter since the last
// check, whichever comes first, the next ownsSaved also reads the snapshot row
// and compares its Gen. It costs one read of the large row per that many
// saves, instead of one per poll.
const (
	verifyEvery = 8
	verifyAfter = 5 * time.Minute
)

// Stamp is the version of a file or object a done name was published as: its
// size and modification time from the listing, and the store's own version
// when it has one. A done name whose listing shows another stamp was replaced,
// and is published again. Compare stamps with a Match, not ==.
type Stamp struct {
	Size      int64 `json:"size"`
	MTime     int64 `json:"mtime"`
	MTimeNsec int64 `json:"mtime_nsec"`
	// Version is the object's ETag (S3, Azure) or generation (GCS), empty
	// for files and for stamps saved before #718. It tells apart two objects
	// with the same size written in the same second. Old code reading a saved
	// row ignores the field.
	Version string `json:"version,omitempty"`
}

// Match is how two stamps of one name are compared.
type Match int

const (
	// MatchTime: the same size and modification time, and the same version
	// when both stamps have one. Files, S3 and Azure.
	MatchTime Match = iota
	// MatchVersion: the same size and version when both stamps have one,
	// whatever the modification time; otherwise as MatchTime. For GCS, whose
	// modification time also changes on a metadata-only edit while the
	// generation stays (#718).
	MatchVersion
)

// Same reports whether a and b are the same version of a name. A stamp
// without a version (saved before #718) matches on size and modification
// time alone, so a done set saved by an older release keeps its names.
func (m Match) Same(a, b Stamp) bool {
	if a.Size != b.Size {
		return false
	}
	if a.Version != "" && b.Version != "" {
		if a.Version != b.Version {
			return false
		}
		if m == MatchVersion {
			return true
		}
	}
	return a.MTime == b.MTime && a.MTimeNsec == b.MTimeNsec
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
	// unverified counts the changes-only saves since ownsSaved last compared
	// the snapshot's Gen with gen; verifiedAt is when it did, or wrote gen.
	unverified int
	verifiedAt time.Time
	// now is the clock; tests replace it.
	now func() time.Time
	// present is the last listing passed to Observe.
	present map[string]Stamp
	// undecodable is the decode error of a row the last Load set aside.
	undecodable error
	// retry holds the names whose after-action failed (#640); memory only.
	retry map[string]retryState
	// match compares stamps; MatchTime unless UseMatch says otherwise.
	match Match
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
		now:    time.Now,
	}
}

// UseMatch sets how stamps are compared (#718). Call it before the first
// Done.
func (s *Set) UseMatch(m Match) { s.match = m }

// Loaded reports whether the saved set has been read. Until it has, the
// consumer must not publish: it can't tell what is already done.
func (s *Set) Loaded() bool { return s.loaded }

// Len is the number of done names.
func (s *Set) Len() int { return len(s.done) }

// Load reads the saved set. found is false on a first start: no saved set, or
// one for another scope. Then the Set starts empty and its first Save records
// it. On an error the Set is left as it was and stays unloaded.
//
// A row that is there but can't be decoded is not an error (#617): it carries
// nothing anyone can read. A snapshot like that counts as no saved set, so
// found is false and the consumer follows its first-start rule; a changes row
// like that is dropped and the snapshot under it is used. Either way
// Undecodable returns the cause and the first Save replaces the rows.
func (s *Set) Load(ctx context.Context) (found bool, err error) {
	if s.store == nil {
		s.loaded = true
		return false, nil
	}
	saved, found, undecodable, err := s.loadSaved(ctx)
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
	s.undecodable = undecodable
	// A first start is saved even if nothing is published, so the next start
	// is not a first one. So is a damaged row, so it does not stay damaged.
	s.dirty = !found || undecodable != nil
	return found, nil
}

// Undecodable returns why the last Load set a saved row aside, or nil if every
// row it read decoded. The consumer logs it once, after that Load.
func (s *Set) Undecodable() error { return s.undecodable }

// Observe takes the poll's listing: name to current stamp. Done names missing
// from it are no longer done, so a file deleted and added again is published
// again.
func (s *Set) Observe(listing map[string]Stamp) {
	s.present = listing
	// A name gone from the listing needs no after-action any more, whoever
	// removed it (#640).
	for name := range s.retry {
		if _, ok := listing[name]; !ok {
			delete(s.retry, name)
		}
	}
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
	return ok && s.match.Same(got, st)
}

// Forget makes name not done, and drops its retries: what is under it now is
// not what was published, so the next poll publishes it (#718).
func (s *Set) Forget(name string) {
	delete(s.retry, name)
	if _, ok := s.done[name]; !ok {
		return
	}
	delete(s.done, name)
	s.touch(name)
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
	// A row that can't be decoded has nothing to merge, and the write that
	// follows replaces it.
	saved, found, _, err := s.loadSaved(ctx)
	if err != nil {
		return err
	}
	s.gen = ""
	if !found {
		return nil
	}
	for name, theirs := range saved {
		now, ok := s.present[name]
		if !ok || !s.match.Same(now, theirs) {
			// Gone, or replaced since the other poller published it.
			continue
		}
		if mine, ok := s.done[name]; ok && s.match.Same(mine, theirs) {
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
	if s.store == nil || !s.loaded {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	if !s.dirty {
		// Nothing to write, but a quiet folder may never save again: if the
		// last check is due, make it, so changes written on a snapshot nobody
		// reads are saved properly within verifyAfter (#723).
		if s.gen == "" || !s.verifyDue() {
			return nil
		}
		mine, err := s.ownsSaved(ctx)
		if err != nil {
			return err
		}
		if mine {
			return nil
		}
		s.dirty = true
	}
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
	s.unverified++
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
	s.unverified = 0
	s.verifiedAt = s.now()
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

// verifyDue reports whether the changes-only saves since the last check are
// enough, in number or in time, to compare the snapshot's Gen again.
func (s *Set) verifyDue() bool {
	return s.unverified > 0 && (s.unverified >= verifyEvery || s.now().Sub(s.verifiedAt) >= verifyAfter)
}

// ownsSaved reports whether the saved set is still this Set's: the changes row
// is on the snapshot this Set wrote last. Only the Set that wrote a snapshot
// writes changes on it, and Save checks this before each such write, so then
// nobody else has saved since. That holds only if no other snapshot came in
// between this read and the write before it, or with its changes reset
// failing; so once changes-only saves are due a check (verifyEvery,
// verifyAfter) the snapshot's Gen is compared too (#723).
func (s *Set) ownsSaved(ctx context.Context) (bool, error) {
	if s.gen == "" {
		return false, nil
	}
	ch, found, err := s.loadChanges(ctx)
	if errors.Is(err, ErrUndecodable) {
		// Not this Set's: it wrote a readable one.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !found || ch.Base != s.gen {
		return false, nil
	}
	if !s.verifyDue() {
		return true, nil
	}
	var snap snapshotState
	found, err = s.read(ctx, s.key.NodeID, &snap)
	if errors.Is(err, ErrUndecodable) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the done set: %w", err)
	}
	if !found || snap.Gen != s.gen {
		return false, nil
	}
	s.unverified = 0
	s.verifiedAt = s.now()
	return true, nil
}

// loadSaved reads the snapshot, and the changes row on top of it when that
// row's Base is the snapshot's Gen. A row that is there but can't be decoded
// is not an error: undecodable is its cause. An undecodable snapshot is no
// saved set (found is false); an undecodable changes row leaves the snapshot
// as it is (#617). Any other error from the store is returned.
func (s *Set) loadSaved(ctx context.Context) (saved map[string]Stamp, found bool, undecodable, err error) {
	var st snapshotState
	found, err = s.read(ctx, s.key.NodeID, &st)
	if errors.Is(err, ErrUndecodable) {
		return nil, false, fmt.Errorf("read the done set: %w", err), nil
	}
	if err != nil {
		return nil, false, nil, fmt.Errorf("read the done set: %w", err)
	}
	if !found || st.Scope != s.scope {
		return nil, false, nil, nil
	}
	saved = make(map[string]Stamp, len(st.Files))
	for name, f := range st.Files {
		saved[name] = f
	}
	if st.Gen == "" {
		return saved, true, nil, nil
	}
	ch, chFound, err := s.loadChanges(ctx)
	if errors.Is(err, ErrUndecodable) {
		return saved, true, err, nil
	}
	if err != nil {
		return nil, false, nil, err
	}
	if !chFound || ch.Base != st.Gen {
		// None yet, or stale: written on an older snapshot.
		return saved, true, nil, nil
	}
	for name, f := range ch.Files {
		saved[name] = f
	}
	for _, name := range ch.Gone {
		delete(saved, name)
	}
	return saved, true, nil, nil
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
		return false, fmt.Errorf("%w: %w", ErrUndecodable, err)
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
