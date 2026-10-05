package pairing

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

func peerFor(t *testing.T, name string) (Peer, *identity.Identity) {
	t.Helper()
	id := newIdentity(t)
	return Peer{ID: id.ID(), Name: name, PublicKey: id.PublicKey(), Added: time.Unix(1700000000, 0).UTC()}, id
}

func TestStoreAddGetListRemove(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := peerFor(t, "Bravo")
	a, _ := peerFor(t, "Alpha")
	for _, p := range []Peer{b, a} {
		if err := s.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Allow(a.ID) || !s.Allow(b.ID) {
		t.Fatal("added peers are not allowed")
	}
	if s.Allow("someoneelse") {
		t.Fatal("unknown id allowed")
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Bravo" {
		t.Fatalf("List = %+v", list)
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if s.Allow(a.ID) {
		t.Fatal("removed peer still allowed")
	}
	if err := s.Remove("missing"); err != nil {
		t.Fatalf("removing an unknown id should not fail: %v", err)
	}
}

func TestStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "peers.json")
	s, _ := OpenStore(path)
	p, _ := peerFor(t, "Phone")
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Get(p.ID)
	if !ok || got.Name != "Phone" || !got.PublicKey.Equal(p.PublicKey) || !got.Added.Equal(p.Added) {
		t.Fatalf("reloaded peer = %+v, %v", got, ok)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("peers file mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestStoreAddReplaces(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "peers.json"))
	p, _ := peerFor(t, "Old name")
	s.Add(p)
	p.Name = "New name"
	s.Add(p)
	if l := s.List(); len(l) != 1 || l[0].Name != "New name" {
		t.Fatalf("List = %+v", l)
	}
}

func TestStoreRejectsMismatchedIDAndKey(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "peers.json"))
	p, _ := peerFor(t, "Mallory")
	other, _ := peerFor(t, "Victim")
	p.ID = other.ID // claim someone else's id with our own key
	if err := s.Add(p); err == nil {
		t.Fatal("accepted an id that does not match the key")
	}
	if s.Allow(other.ID) {
		t.Fatal("victim id became allowed")
	}
	if err := s.Add(Peer{ID: "x", PublicKey: []byte{1, 2}}); err == nil {
		t.Fatal("accepted a short key")
	}
}

func TestStoreFailedWriteLeavesStateUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peers.json")
	s, _ := OpenStore(path)
	p, _ := peerFor(t, "First")
	if err := s.Add(p); err != nil {
		t.Fatal(err)
	}
	// Make the target unwritable by replacing the file with a non-empty directory.
	os.Remove(path)
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	q, _ := peerFor(t, "Second")
	if err := s.Add(q); err == nil {
		t.Fatal("expected the write to fail")
	}
	if s.Allow(q.ID) {
		t.Fatal("peer became visible even though saving failed")
	}
	if !s.Allow(p.ID) {
		t.Fatal("existing peer was lost")
	}
}

func TestOpenStoreCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := OpenStore(path); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("want ErrCorruptStore, got %v", err)
	}

	// An entry whose id does not match its key must be refused on load too.
	p, _ := peerFor(t, "A")
	other, _ := peerFor(t, "B")
	good, _ := OpenStore(filepath.Join(t.TempDir(), "ok.json"))
	good.Add(p)
	data, _ := os.ReadFile(good.path)
	tampered := string(data)
	tampered = replaceOnce(tampered, string(p.ID), string(other.ID))
	os.WriteFile(path, []byte(tampered), 0o600)
	if _, err := OpenStore(path); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("tampered file: want ErrCorruptStore, got %v", err)
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestOpenStoreMissingFileIsEmpty(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(s.List()) != 0 {
		t.Fatalf("got %v, %v", s, err)
	}
}

func TestCleanName(t *testing.T) {
	id := identity.DeviceID("abcdefghijklmnop")
	cases := []struct{ in, want string }{
		{"  Pixel 8  ", "Pixel 8"},
		{"Bad\x00\x1b[31mName\n", "Bad[31mName"},
		{"", "device-abcdefgh"},
		{" \t\n", "device-abcdefgh"},
		{string(make([]rune, 0)), "device-abcdefgh"},
	}
	for _, c := range cases {
		if got := cleanName(c.in, id); got != c.want {
			t.Errorf("cleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "é"
	}
	if got := []rune(cleanName(long, id)); len(got) != maxNameRunes {
		t.Errorf("long name has %d runes, want %d", len(got), maxNameRunes)
	}
}
