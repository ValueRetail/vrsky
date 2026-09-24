package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// State is the agent's memory of uploads, kept on disk so a restart neither
// loses nor repeats work.
//
// Each entry is keyed by pipeline, folder, file name, size and modification
// time. It carries the upload ID — reused on every retry of that exact file,
// so VRSky drops a duplicate if a retry follows an upload whose answer was lost
// — and whether the upload finished. A file whose upload finished but that
// could not then be moved or deleted (still locked, say) is recognised on the
// next scan, even after a restart, and not uploaded again.
type State struct {
	mu   sync.Mutex
	path string
	data map[string]*uploadRecord
}

type uploadRecord struct {
	UploadID string    `json:"upload_id"`
	Done     bool      `json:"done"`
	Since    time.Time `json:"since"`
}

// stateTTL drops records for files that have long since gone, so the file
// does not grow forever.
const stateTTL = 7 * 24 * time.Hour

// LoadState opens (or starts) the state file in dataDir.
func LoadState(dataDir string) (*State, error) {
	s := &State{path: filepath.Join(dataDir, "state.json"), data: map[string]*uploadRecord{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		// A corrupt state file must not stop the agent; the cost is at most
		// re-uploading files that are still in their folders, which VRSky
		// de-duplicates if the IDs had been recent.
		s.data = map[string]*uploadRecord{}
	}
	for k, r := range s.data {
		if time.Since(r.Since) > stateTTL {
			delete(s.data, k)
		}
	}
	return s, nil
}

// uploadID returns the stable upload ID for key, creating and persisting one.
func (s *State) uploadID(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.data[key]; ok {
		return r.UploadID, nil
	}
	s.data[key] = &uploadRecord{UploadID: uuid.NewString(), Since: time.Now()}
	return s.data[key].UploadID, s.saveLocked()
}

func (s *State) isDone(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[key]
	return ok && r.Done
}

func (s *State) markDone(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.data[key]; ok {
		r.Done = true
	} else {
		s.data[key] = &uploadRecord{UploadID: uuid.NewString(), Done: true, Since: time.Now()}
	}
	return s.saveLocked()
}

// forget drops keys once their file has been moved or deleted.
func (s *State) forget(keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.data, k)
	}
	return s.saveLocked()
}

func (s *State) saveLocked() error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
