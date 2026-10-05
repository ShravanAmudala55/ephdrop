// Package shelf is the place where this device keeps the files it is sharing.
//
// Each file is copied into the shelf's own folder, so the original can be moved
// or deleted without breaking the share. Every file has an expiry time. Expired
// files are never listed or opened, and Sweep deletes them from disk.
//
// The shelf only holds this device's own files. Lists that arrive from other
// devices are kept elsewhere, as plain Entry values.
package shelf

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/internal/atomicfile"
)

const (
	// DefaultTTL is how long a file lives if the caller does not say.
	DefaultTTL = 24 * time.Hour
	// MaxTTL is the longest a file may live.
	MaxTTL = 7 * 24 * time.Hour
	// MaxNameBytes is the longest file name kept.
	MaxNameBytes = 255

	idBytes = 16
)

var (
	// ErrNotFound means there is no live file with that id.
	ErrNotFound = errors.New("shelf: no such file")
	// ErrBadTTL means the lifetime is zero, negative or above MaxTTL.
	ErrBadTTL = errors.New("shelf: bad lifetime")
	// ErrTooLarge means the file is bigger than the limit given to Add.
	ErrTooLarge = errors.New("shelf: file too large")
	// ErrCorrupt means the index file exists but cannot be understood.
	ErrCorrupt = errors.New("shelf: corrupt index")
)

// Entry describes one shared file.
type Entry struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Size    int64             `json:"size"`
	SHA256  string            `json:"sha256"` // lowercase hex
	Owner   identity.DeviceID `json:"owner"`
	Created time.Time         `json:"created"`
	Expires time.Time         `json:"expires"`
}

// Expired reports whether the entry is past its expiry at time now.
func (e Entry) Expired(now time.Time) bool { return !now.Before(e.Expires) }

type indexFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Shelf is safe for concurrent use.
type Shelf struct {
	dir   string
	owner identity.DeviceID
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]Entry
}

// Open opens the shelf in dir, creating it if needed. Files added are owned by
// owner. Blob files that no entry refers to, and entries whose file is gone, are
// cleaned up.
func Open(dir string, owner identity.DeviceID) (*Shelf, error) {
	return open(dir, owner, time.Now)
}

func open(dir string, owner identity.DeviceID, now func() time.Time) (*Shelf, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		return nil, err
	}
	s := &Shelf{dir: dir, owner: owner, now: now, entries: map[string]Entry{}}
	data, err := os.ReadFile(s.indexPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var f indexFile
		if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
			return nil, ErrCorrupt
		}
		for _, e := range f.Entries {
			if !validID(e.ID) {
				return nil, ErrCorrupt
			}
			s.entries[e.ID] = e
		}
	}
	if err := s.reconcile(); err != nil {
		return nil, err
	}
	return s, nil
}

// reconcile drops entries without a blob and blobs without an entry.
func (s *Shelf) reconcile() error {
	changed := false
	for id, e := range s.entries {
		fi, err := os.Stat(s.blobPath(id))
		if err != nil || fi.Size() != e.Size {
			delete(s.entries, id)
			_ = os.Remove(s.blobPath(id))
			changed = true
		}
	}
	items, err := os.ReadDir(filepath.Join(s.dir, "blobs"))
	if err != nil {
		return err
	}
	for _, it := range items {
		if _, ok := s.entries[it.Name()]; !ok {
			_ = os.Remove(filepath.Join(s.dir, "blobs", it.Name()))
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

// Add copies r into the shelf. maxSize limits the number of bytes read (zero
// means no limit). ttl is the lifetime, or zero for DefaultTTL.
func (s *Shelf) Add(r io.Reader, name string, ttl time.Duration, maxSize int64) (Entry, error) {
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < 0 || ttl > MaxTTL {
		return Entry{}, ErrBadTTL
	}
	id, err := newID()
	if err != nil {
		return Entry{}, err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "blobs"), ".incoming-*")
	if err != nil {
		return Entry{}, err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	src := r
	if maxSize > 0 {
		src = io.LimitReader(r, maxSize+1)
	}
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		return Entry{}, err
	}
	if maxSize > 0 && n > maxSize {
		return Entry{}, ErrTooLarge
	}
	if err := tmp.Chmod(0o600); err != nil {
		return Entry{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Entry{}, err
	}
	if err := tmp.Close(); err != nil {
		return Entry{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	e := Entry{
		ID:      id,
		Name:    CleanName(name),
		Size:    n,
		SHA256:  hex.EncodeToString(h.Sum(nil)),
		Owner:   s.owner,
		Created: now,
		Expires: now.Add(ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(tmpName, s.blobPath(id)); err != nil {
		return Entry{}, err
	}
	done = true
	s.entries[id] = e
	if err := s.saveLocked(); err != nil {
		delete(s.entries, id)
		os.Remove(s.blobPath(id))
		return Entry{}, err
	}
	return e, nil
}

// List returns the live files, newest first.
func (s *Shelf) List() []Entry {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if !e.Expired(now) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns the entry for id, or ErrNotFound if it is missing or expired.
func (s *Shelf) Get(id string) (Entry, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok || e.Expired(now) {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

// Open returns the file's contents for reading. The caller closes it. A file
// opened just before it expires can still be read to the end.
func (s *Shelf) Open(id string) (*os.File, Entry, error) {
	e, err := s.Get(id)
	if err != nil {
		return nil, Entry{}, err
	}
	f, err := os.Open(s.blobPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Entry{}, ErrNotFound
		}
		return nil, Entry{}, err
	}
	return f, e, nil
}

// Remove deletes a file at once. It returns ErrNotFound if there is none.
func (s *Shelf) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return ErrNotFound
	}
	delete(s.entries, id)
	if err := s.saveLocked(); err != nil {
		return err
	}
	if err := os.Remove(s.blobPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep deletes every expired file and returns how many it removed.
func (s *Shelf) Sweep() (int, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	for id, e := range s.entries {
		if e.Expired(now) {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	for _, id := range gone {
		delete(s.entries, id)
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	for _, id := range gone {
		_ = os.Remove(s.blobPath(id))
	}
	return len(gone), nil
}

func (s *Shelf) saveLocked() error {
	f := indexFile{Version: 1, Entries: make([]Entry, 0, len(s.entries))}
	for _, e := range s.entries {
		f.Entries = append(f.Entries, e)
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].ID < f.Entries[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.indexPath(), data, 0o600)
}

func (s *Shelf) indexPath() string { return filepath.Join(s.dir, "index.json") }

func (s *Shelf) blobPath(id string) string { return filepath.Join(s.dir, "blobs", id) }

var idEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

func newID() (string, error) {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("shelf: random id: %w", err)
	}
	return strings.ToLower(idEnc.EncodeToString(b[:])), nil
}

// validID accepts only what newID produces, so an id can never be used to reach
// outside the blobs folder.
func validID(id string) bool {
	if len(id) != idEnc.EncodedLen(idBytes) {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// ValidID reports whether id has the shape of a file id.
func ValidID(id string) bool { return validID(id) }

// CleanName makes a file name safe to store and show. It keeps only the last
// path element, removes control characters, and cuts the result to
// MaxNameBytes without splitting a character. An empty result becomes "file".
func CleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	for len(name) > MaxNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}
