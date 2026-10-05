package shelf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newShelf(t *testing.T) (*Shelf, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	s, err := open(dir, "owner1", c.Now)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, dir
}

func add(t *testing.T, s *Shelf, name, body string, ttl time.Duration) Entry {
	t.Helper()
	e, err := s.Add(strings.NewReader(body), name, ttl, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func readAll(t *testing.T, s *Shelf, id string) string {
	t.Helper()
	f, _, err := s.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func blobCount(t *testing.T, dir string) int {
	t.Helper()
	items, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func TestAddRecordsDetails(t *testing.T) {
	s, c, _ := newShelf(t)
	body := "hello world"
	e := add(t, s, "greeting.txt", body, 2*time.Hour)
	sum := sha256.Sum256([]byte(body))
	if e.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("hash %s", e.SHA256)
	}
	if e.Size != int64(len(body)) || e.Name != "greeting.txt" || e.Owner != "owner1" {
		t.Errorf("entry %+v", e)
	}
	if !e.Created.Equal(c.Now()) || !e.Expires.Equal(c.Now().Add(2*time.Hour)) {
		t.Errorf("times %v %v", e.Created, e.Expires)
	}
	if !ValidID(e.ID) {
		t.Errorf("id %q", e.ID)
	}
	if got := readAll(t, s, e.ID); got != body {
		t.Errorf("content %q", got)
	}
}

func TestDefaultTTL(t *testing.T) {
	s, _, _ := newShelf(t)
	e := add(t, s, "a", "x", 0)
	if e.Expires.Sub(e.Created) != DefaultTTL {
		t.Errorf("ttl %v", e.Expires.Sub(e.Created))
	}
}

func TestBadTTLRejected(t *testing.T) {
	s, _, dir := newShelf(t)
	for _, ttl := range []time.Duration{-time.Second, MaxTTL + time.Second} {
		_, err := s.Add(strings.NewReader("x"), "a", ttl, 0)
		if !errors.Is(err, ErrBadTTL) {
			t.Errorf("ttl %v: %v", ttl, err)
		}
	}
	if _, err := s.Add(strings.NewReader("x"), "a", MaxTTL, 0); err != nil {
		t.Errorf("max ttl should work: %v", err)
	}
	if n := blobCount(t, dir); n != 1 {
		t.Errorf("blobs %d", n)
	}
}

func TestIDsAreUnique(t *testing.T) {
	s, _, _ := newShelf(t)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		e := add(t, s, "a", "x", 0)
		if seen[e.ID] {
			t.Fatal("duplicate id")
		}
		seen[e.ID] = true
	}
}

func TestSizeLimit(t *testing.T) {
	s, _, dir := newShelf(t)
	if _, err := s.Add(strings.NewReader("12345"), "a", 0, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if n := blobCount(t, dir); n != 0 {
		t.Errorf("leftover blobs %d", n)
	}
	if len(s.List()) != 0 {
		t.Error("entry kept")
	}
	if _, err := s.Add(strings.NewReader("1234"), "a", 0, 4); err != nil {
		t.Errorf("exact size should work: %v", err)
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("disk on fire")
	}
	f.n--
	p[0] = 'x'
	return 1, nil
}

func TestFailedReadLeavesNothingBehind(t *testing.T) {
	s, _, dir := newShelf(t)
	if _, err := s.Add(&failingReader{n: 10}, "a", 0, 0); err == nil {
		t.Fatal("expected error")
	}
	if n := blobCount(t, dir); n != 0 {
		t.Errorf("leftover blobs %d", n)
	}
	if len(s.List()) != 0 {
		t.Error("entry kept")
	}
}

func TestEmptyFile(t *testing.T) {
	s, _, _ := newShelf(t)
	e := add(t, s, "empty", "", 0)
	if e.Size != 0 {
		t.Errorf("size %d", e.Size)
	}
	if got := readAll(t, s, e.ID); got != "" {
		t.Errorf("content %q", got)
	}
}

func TestListHidesExpiredAndSortsNewestFirst(t *testing.T) {
	s, c, _ := newShelf(t)
	short := add(t, s, "short", "1", time.Hour)
	c.Advance(time.Minute)
	long1 := add(t, s, "long1", "2", 5*time.Hour)
	c.Advance(time.Minute)
	long2 := add(t, s, "long2", "3", 5*time.Hour)

	got := s.List()
	if len(got) != 3 || got[0].ID != long2.ID || got[1].ID != long1.ID || got[2].ID != short.ID {
		t.Fatalf("order %+v", got)
	}
	c.Advance(time.Hour) // short is now 62 minutes old
	got = s.List()
	if len(got) != 2 || got[0].ID != long2.ID || got[1].ID != long1.ID {
		t.Fatalf("after expiry %+v", got)
	}
}

func TestExpiryIsExactlyAtTheExpiryTime(t *testing.T) {
	s, c, _ := newShelf(t)
	e := add(t, s, "a", "x", time.Hour)
	c.Advance(time.Hour - time.Second)
	if _, err := s.Get(e.ID); err != nil {
		t.Errorf("one second before: %v", err)
	}
	c.Advance(time.Second)
	if _, err := s.Get(e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("at expiry: %v", err)
	}
}

func TestExpiredFilesCannotBeOpened(t *testing.T) {
	s, c, _ := newShelf(t)
	e := add(t, s, "a", "x", time.Hour)
	c.Advance(2 * time.Hour)
	if _, _, err := s.Open(e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestSweepDeletesOnlyExpired(t *testing.T) {
	s, c, dir := newShelf(t)
	a := add(t, s, "a", "1", time.Hour)
	b := add(t, s, "b", "2", 3*time.Hour)
	add(t, s, "c", "3", time.Hour)
	c.Advance(2 * time.Hour)
	n, err := s.Sweep()
	if err != nil || n != 2 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if blobCount(t, dir) != 1 {
		t.Errorf("blobs %d", blobCount(t, dir))
	}
	if _, err := os.Stat(s.blobPath(a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Error("expired blob remains")
	}
	if got := readAll(t, s, b.ID); got != "2" {
		t.Errorf("survivor %q", got)
	}
	if n, _ := s.Sweep(); n != 0 {
		t.Errorf("second sweep %d", n)
	}
}

func TestSweepIsRememberedAfterReopen(t *testing.T) {
	s, c, dir := newShelf(t)
	add(t, s, "a", "1", time.Hour)
	c.Advance(2 * time.Hour)
	if _, err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	// Read the index itself: reopening would hide the problem, because Open
	// drops entries whose file is gone.
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"name"`) {
		t.Errorf("swept entry still in the index: %s", data)
	}
}

func TestTimesAreWholeSeconds(t *testing.T) {
	s, c, _ := newShelf(t)
	c.Advance(123456789 * time.Nanosecond)
	e := add(t, s, "a", "x", 0)
	if e.Created.Nanosecond() != 0 || e.Expires.Nanosecond() != 0 {
		t.Errorf("times %v %v", e.Created, e.Expires)
	}
}

func TestRemove(t *testing.T) {
	s, _, dir := newShelf(t)
	e := add(t, s, "a", "1", 0)
	if err := s.Remove(e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(e.ID); !errors.Is(err, ErrNotFound) {
		t.Error("still there")
	}
	if blobCount(t, dir) != 0 {
		t.Error("blob remains")
	}
	if err := s.Remove(e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, c, dir := newShelf(t)
	e := add(t, s, "keep.txt", "data", 0)
	s2, err := open(dir, "owner1", c.Now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get(e.ID)
	if err != nil || got != e {
		t.Fatalf("got %+v, %v", got, err)
	}
	if readAll(t, s2, e.ID) != "data" {
		t.Error("content")
	}
}

func TestOpenCleansUpStrayFilesAndMissingBlobs(t *testing.T) {
	s, c, dir := newShelf(t)
	keep := add(t, s, "keep", "1", 0)
	lost := add(t, s, "lost", "2", 0)
	if err := os.Remove(s.blobPath(lost.ID)); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "blobs", ".incoming-crash")
	if err := os.WriteFile(stray, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := open(dir, "owner1", c.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Get(lost.ID); !errors.Is(err, ErrNotFound) {
		t.Error("entry without blob kept")
	}
	if _, err := s2.Get(keep.ID); err != nil {
		t.Error("good entry lost")
	}
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Error("stray file kept")
	}
	// and the cleanup was saved
	s3, _ := open(dir, "owner1", c.Now)
	if len(s3.entries) != 1 {
		t.Errorf("entries %d", len(s3.entries))
	}
}

func TestOpenDetectsTruncatedBlob(t *testing.T) {
	s, c, dir := newShelf(t)
	e := add(t, s, "a", "12345", 0)
	if err := os.WriteFile(s.blobPath(e.ID), []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, _ := open(dir, "owner1", c.Now)
	if _, err := s2.Get(e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("truncated blob kept: %v", err)
	}
}

func TestCorruptIndex(t *testing.T) {
	cases := map[string]string{
		"garbage":       "not json",
		"wrong version": `{"version":2,"entries":[]}`,
		"bad id":        `{"version":1,"entries":[{"id":"../../etc/passwd"}]}`,
	}
	for name, content := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, "o"); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestFilePermissionsArePrivate(t *testing.T) {
	s, _, dir := newShelf(t)
	e := add(t, s, "a", "x", 0)
	for _, p := range []string{s.blobPath(e.ID), filepath.Join(dir, "index.json")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", p, fi.Mode().Perm())
		}
	}
}

func TestValidID(t *testing.T) {
	s, _, _ := newShelf(t)
	e := add(t, s, "a", "x", 0)
	bad := []string{"", "short", e.ID + "a", e.ID[:25] + "/", strings.ToUpper(e.ID), "../" + e.ID[3:], e.ID[:25] + "1", e.ID[:25] + "8"}
	for _, id := range bad {
		if ValidID(id) {
			t.Errorf("%q accepted", id)
		}
	}
	if !ValidID(e.ID) {
		t.Error("real id rejected")
	}
}

func TestGetWithUnknownIDs(t *testing.T) {
	s, _, _ := newShelf(t)
	for _, id := range []string{"", "../index.json", "nope"} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
		if _, _, err := s.Open(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("open %q: %v", id, err)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"photo.jpg", "photo.jpg"},
		{"/etc/passwd", "passwd"},
		{`C:\Users\me\doc.pdf`, "doc.pdf"},
		{"../../x.txt", "x.txt"},
		{"dir/", "file"},
		{"", "file"},
		{"..", "file"},
		{".", "file"},
		{"  spaced name  ", "spaced name"},
		{"bad\x00na\nme\x1b.txt", "badname.txt"},
		{"tab\there", "tabhere"},
		{"bad\xffutf8", "badutf8"},
		{"ünï©ödé.txt", "ünï©ödé.txt"},
	}
	for _, c := range cases {
		if got := CleanName(c.in); got != c.want {
			t.Errorf("CleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanNameLimitsLengthWithoutBreakingCharacters(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	got := CleanName(long)
	if len(got) > MaxNameBytes {
		t.Errorf("len %d", len(got))
	}
	if strings.ContainsRune(got, '\uFFFD') || !strings.HasPrefix(long, got) || len(got) < MaxNameBytes-1 {
		t.Errorf("bad cut: %d bytes", len(got))
	}
	ascii := CleanName(strings.Repeat("a", 1000))
	if len(ascii) != MaxNameBytes {
		t.Errorf("ascii len %d", len(ascii))
	}
}

func TestStoredNameIsCleaned(t *testing.T) {
	s, _, _ := newShelf(t)
	e := add(t, s, "../../evil\n.txt", "x", 0)
	if e.Name != "evil.txt" {
		t.Errorf("name %q", e.Name)
	}
}

func TestOpenedFileStaysReadableAfterRemove(t *testing.T) {
	s, _, _ := newShelf(t)
	e := add(t, s, "a", "still here", 0)
	f, _, err := s.Open(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := s.Remove(e.ID); err != nil {
		t.Skipf("platform does not allow removing an open file: %v", err)
	}
	b, _ := io.ReadAll(f)
	if !bytes.Equal(b, []byte("still here")) {
		t.Errorf("read %q", b)
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _, _ := newShelf(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				e, err := s.Add(strings.NewReader("data"), "f", 0, 0)
				if err != nil {
					t.Error(err)
					return
				}
				s.List()
				if _, err := s.Get(e.ID); err != nil {
					t.Error(err)
				}
				if j%2 == 0 {
					if err := s.Remove(e.ID); err != nil {
						t.Error(err)
					}
				}
				if _, err := s.Sweep(); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if got := len(s.List()); got != 40 {
		t.Errorf("entries %d, want 40", got)
	}
}
