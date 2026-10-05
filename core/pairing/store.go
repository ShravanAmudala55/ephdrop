package pairing

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/internal/atomicfile"
)

// ErrCorruptStore means the peers file exists but cannot be understood.
var ErrCorruptStore = errors.New("pairing: corrupt peers file")

// Peer is a paired device.
type Peer struct {
	ID        identity.DeviceID `json:"id"`
	Name      string            `json:"name"`
	PublicKey ed25519.PublicKey `json:"key"`
	Added     time.Time         `json:"added"`
}

type storeFile struct {
	Version int    `json:"version"`
	Peers   []Peer `json:"peers"`
}

// Store is the persistent list of paired devices. It is safe for concurrent use.
type Store struct {
	path  string
	mu    sync.RWMutex
	peers map[identity.DeviceID]Peer
}

// OpenStore loads the store at path. A missing file is an empty store.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, peers: map[identity.DeviceID]Peer{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pairing: read peers: %w", err)
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptStore, err)
	}
	for _, p := range f.Peers {
		p, err := normalize(p)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptStore, err)
		}
		s.peers[p.ID] = p
	}
	return s, nil
}

// normalize validates a peer and returns a cleaned copy. The id must match the
// key, so a stored id can never be attached to someone else's key.
func normalize(p Peer) (Peer, error) {
	key, err := identity.ParsePublicKey(p.PublicKey)
	if err != nil {
		return Peer{}, err
	}
	if identity.IDFromPublicKey(key) != p.ID {
		return Peer{}, fmt.Errorf("peer id %q does not match its key", p.ID)
	}
	return Peer{ID: p.ID, Name: cleanName(p.Name, p.ID), PublicKey: key, Added: p.Added}, nil
}

// Add stores a peer, replacing any existing entry with the same id. The change
// is written to disk before it becomes visible.
func (s *Store) Add(p Peer) error {
	p, err := normalize(p)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.copyLocked()
	next[p.ID] = p
	return s.commitLocked(next)
}

// Remove forgets a peer. Removing an unknown id is not an error.
func (s *Store) Remove(id identity.DeviceID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peers[id]; !ok {
		return nil
	}
	next := s.copyLocked()
	delete(next, id)
	return s.commitLocked(next)
}

// Get returns the peer with the given id.
func (s *Store) Get(id identity.DeviceID) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[id]
	return p, ok
}

// Allow reports whether id is a paired device. Pass it to identity.PinnedConfig.
func (s *Store) Allow(id identity.DeviceID) bool {
	_, ok := s.Get(id)
	return ok
}

// List returns all peers ordered by name, then id.
func (s *Store) List() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Store) copyLocked() map[identity.DeviceID]Peer {
	next := make(map[identity.DeviceID]Peer, len(s.peers)+1)
	for k, v := range s.peers {
		next[k] = v
	}
	return next
}

func (s *Store) commitLocked(next map[identity.DeviceID]Peer) error {
	f := storeFile{Version: 1, Peers: make([]Peer, 0, len(next))}
	for _, p := range next {
		f.Peers = append(f.Peers, p)
	}
	sort.Slice(f.Peers, func(i, j int) bool { return f.Peers[i].ID < f.Peers[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("pairing: encode peers: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("pairing: create data dir: %w", err)
	}
	if err := atomicfile.Write(s.path, data, 0o600); err != nil {
		return fmt.Errorf("pairing: save peers: %w", err)
	}
	s.peers = next
	return nil
}

// cleanName strips control characters, limits the length and supplies a
// fallback so a peer always has a printable name. Names come from other
// devices, so they are never trusted as-is.
func cleanName(name string, id identity.DeviceID) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsControl(r) {
			continue
		}
		if n >= maxNameRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "device-" + id.Short()
	}
	return out
}
