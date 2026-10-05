package transfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
)

type device struct {
	id   *identity.Identity
	cert tls.Certificate
}

func newDevice(t *testing.T) device {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := id.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	return device{id, cert}
}

type rig struct {
	server, client device
	shelf          *shelf.Shelf
	srv            *Server
	cl             *Client
	addr           string
	logs           *logBuf
}

type logBuf struct {
	mu sync.Mutex
	b  []string
}

func (l *logBuf) add(f string, a ...any) {
	l.mu.Lock()
	l.b = append(l.b, f)
	l.mu.Unlock()
}

func newRig(t *testing.T) *rig { return newRigWith(t, nil) }

// newRigWith is newRig, but lets the test adjust the server before it starts.
func newRigWith(t *testing.T, tweak func(*Server)) *rig {
	t.Helper()
	r := &rig{server: newDevice(t), client: newDevice(t), logs: &logBuf{}}
	var err error
	r.shelf, err = shelf.Open(t.TempDir(), r.server.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	clientID := r.client.id.ID()
	r.srv = &Server{
		Cert:  r.server.cert,
		Allow: func(id identity.DeviceID) bool { return id == clientID },
		Shelf: r.shelf,
		Logf:  r.logs.add,
	}
	r.cl = &Client{Cert: r.client.cert}
	if tweak != nil {
		tweak(r.srv)
	}
	r.addr = r.serve(t, r.srv)
	return r
}

func (r *rig) serve(t *testing.T, s *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	return ln.Addr().String()
}

func (r *rig) put(t *testing.T, name, body string) shelf.Entry {
	t.Helper()
	e, err := r.shelf.Add(strings.NewReader(body), name, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Name())
	}
	return out
}

func TestListShowsFiles(t *testing.T) {
	r := newRig(t)
	a := r.put(t, "a.txt", "aaa")
	b := r.put(t, "b.txt", "bbbb")
	got, err := r.cl.List(ctxT(t), r.addr, r.server.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries", len(got))
	}
	byID := map[string]shelf.Entry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID[a.ID].SHA256 != a.SHA256 || byID[b.ID].Size != 4 || byID[b.ID].Name != "b.txt" {
		t.Errorf("entries %+v", got)
	}
}

func TestListEmpty(t *testing.T) {
	r := newRig(t)
	got, err := r.cl.List(ctxT(t), r.addr, r.server.id.ID())
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestPullRoundTrip(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "hello.txt", "hello over the wire")
	dir := t.TempDir()
	path, got, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "hello.txt" {
		t.Errorf("path %s", path)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "hello over the wire" {
		t.Errorf("content %q", b)
	}
	if got.ID != e.ID || got.SHA256 != e.SHA256 {
		t.Errorf("entry %+v", got)
	}
	if l := leftovers(t, dir); len(l) != 1 {
		t.Errorf("dir has %v", l)
	}
}

func TestPullLargeFile(t *testing.T) {
	r := newRig(t)
	data := make([]byte, 6<<20+123)
	rand.Read(data)
	e, err := r.shelf.Add(bytes.NewReader(data), "big.bin", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Error("content differs")
	}
}

func TestPullEmptyFile(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "empty", "")
	path, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() != 0 {
		t.Error("not empty")
	}
}

func TestPullCreatesMissingFolder(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "a", "x")
	dir := filepath.Join(t.TempDir(), "new", "folder")
	if _, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir); err != nil {
		t.Fatal(err)
	}
}

func TestPullNeverOverwrites(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "doc.txt", "new")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("mine"), 0o600)
	p1, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != "doc (1).txt" || filepath.Base(p2) != "doc (2).txt" {
		t.Errorf("names %s %s", p1, p2)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "doc.txt")); string(b) != "mine" {
		t.Error("original overwritten")
	}
}

func TestClaimName(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ name, first, second string }{
		{"a.tar.gz", "a.tar.gz", "a.tar (1).gz"},
		{".bashrc", ".bashrc", ".bashrc (1)"},
		{"noext", "noext", "noext (1)"},
	}
	for _, c := range cases {
		p, err := claimName(dir, c.name)
		if err != nil || filepath.Base(p) != c.first {
			t.Errorf("%s first: %s %v", c.name, p, err)
		}
		p, err = claimName(dir, c.name)
		if err != nil || filepath.Base(p) != c.second {
			t.Errorf("%s second: %s %v", c.name, p, err)
		}
	}
}

func TestPullNotFound(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	// a valid looking id that does not exist
	_, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), strings.Repeat("a", 26), dir)
	if !errors.Is(err, shelf.ErrNotFound) {
		t.Errorf("got %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftovers %v", l)
	}
}

func TestPullExpiredFileIsNotFound(t *testing.T) {
	r := newRig(t)
	e, err := r.shelf.Add(strings.NewReader("x"), "a", time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)
	_, _, err = r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, t.TempDir())
	if !errors.Is(err, shelf.ErrNotFound) {
		t.Errorf("got %v", err)
	}
	list, _ := r.cl.List(ctxT(t), r.addr, r.server.id.ID())
	if len(list) != 0 {
		t.Errorf("expired file listed: %+v", list)
	}
}

func TestPullRejectsBadIDLocally(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"", "../x", "short"} {
		if _, _, err := r.cl.Pull(ctxT(t), "127.0.0.1:1", r.server.id.ID(), id, t.TempDir()); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

// rawRequest sends one line to the server as a paired device and returns
// the first reply line.
func rawRequest(t *testing.T, r *rig, line string) (string, error) {
	t.Helper()
	cfg := identity.PinnedConfig(r.client.cert, func(id identity.DeviceID) bool { return id == r.server.id.ID() })
	conn, err := tls.Dial("tcp", r.addr, cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(line)); err != nil {
		return "", err
	}
	s, err := bufio.NewReader(conn).ReadString('\n')
	return s, err
}

func TestServerRejectsBadRequests(t *testing.T) {
	r := newRig(t)
	cases := map[string]string{
		"garbage":    "not json\n",
		"unknown op": `{"op":"delete"}` + "\n",
		"empty op":   "{}\n",
		"bad id":     `{"op":"get","id":"../../etc/passwd"}` + "\n",
		"missing id": `{"op":"get"}` + "\n",
		"upper id":   `{"op":"get","id":"` + strings.Repeat("A", 26) + `"}` + "\n",
	}
	for name, line := range cases {
		reply, err := rawRequest(t, r, line)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var resp response
		if json.Unmarshal([]byte(reply), &resp) != nil || resp.OK || resp.Code != "bad_request" {
			t.Errorf("%s: reply %q", name, reply)
		}
	}
}

func TestServerClosesOnOversizedRequest(t *testing.T) {
	r := newRig(t)
	_, err := rawRequest(t, r, strings.Repeat("x", 5000)+"\n")
	if err == nil {
		t.Error("oversized request got a reply")
	}
}

func TestUnpairedClientIsRejected(t *testing.T) {
	r := newRig(t)
	stranger := &Client{Cert: newDevice(t).cert}
	if _, err := stranger.List(ctxT(t), r.addr, r.server.id.ID()); err == nil {
		t.Error("stranger could list")
	}
	e := r.put(t, "secret", "secret")
	dir := t.TempDir()
	if _, _, err := stranger.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir); err == nil {
		t.Error("stranger could pull")
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftovers %v", l)
	}
}

func TestClientChecksWhoItIsTalkingTo(t *testing.T) {
	r := newRig(t)
	other := newDevice(t)
	_, err := r.cl.List(ctxT(t), r.addr, other.id.ID())
	if !errors.Is(err, ErrWrongPeer) {
		t.Errorf("got %v", err)
	}
}

func TestPullHonoursMaxSize(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "a", "0123456789")
	r.cl.MaxSize = 9
	dir := t.TempDir()
	if _, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v", err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftovers %v", l)
	}
	r.cl.MaxSize = 10
	if _, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), e.ID, dir); err != nil {
		t.Errorf("exact size: %v", err)
	}
}

func TestParallelPulls(t *testing.T) {
	r := newRig(t)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, r.put(t, "f", strings.Repeat(string(rune('a'+i)), 100000)).ID)
	}
	var wg sync.WaitGroup
	for round := 0; round < 4; round++ {
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, _, err := r.cl.Pull(ctxT(t), r.addr, r.server.id.ID(), id, t.TempDir())
				if err != nil {
					t.Error(err)
					return
				}
				b, _ := os.ReadFile(p)
				if string(b) != strings.Repeat(string(rune('a'+i)), 100000) {
					t.Error("wrong content")
				}
			}()
		}
	}
	wg.Wait()
}

func TestServeStopsEvenWithAStalledConnection(t *testing.T) {
	r := newRig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.srv.Serve(ctx, ln) }()
	cfg := identity.PinnedConfig(r.client.cert, func(id identity.DeviceID) bool { return id == r.server.id.ID() })
	conn, err := tls.Dial("tcp", ln.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(200 * time.Millisecond) // let the server reach its read
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop while a client was connected")
	}
}

func TestServerDropsClientsThatNeverFinishTheHandshake(t *testing.T) {
	r := newRigWith(t, func(s *Server) { s.handshake = 300 * time.Millisecond })
	conn, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("read succeeded")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("server waited too long for the handshake")
	}
}

type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("pipe broke")
	}
	f.n--
	return len(p), nil
}

func TestServerReportsAFailedSend(t *testing.T) {
	r := newRig(t)
	e := r.put(t, "a", "some content")
	if err := r.srv.get(&failAfter{n: 1}, e.ID); err == nil {
		t.Error("failed send went unnoticed")
	}
}

// ---- misbehaving servers

// fake runs a pinned TLS server that answers each connection with handler.
func fake(t *testing.T, r *rig, handler func(conn net.Conn, req request)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	clientID := r.client.id.ID()
	cfg := identity.PinnedConfig(r.server.cert, func(id identity.DeviceID) bool { return id == clientID })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				tc := tls.Server(c, cfg)
				tc.SetDeadline(time.Now().Add(30 * time.Second))
				if tc.Handshake() != nil {
					return
				}
				line, err := bufio.NewReader(tc).ReadBytes('\n')
				if err != nil {
					return
				}
				var req request
				json.Unmarshal(line, &req)
				handler(tc, req)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sendFile(c net.Conn, e shelf.Entry, body []byte) {
	b, _ := json.Marshal(response{OK: true, Entry: &e})
	c.Write(append(b, '\n'))
	c.Write(body)
}

func goodEntry(id string, body []byte) shelf.Entry {
	return shelf.Entry{ID: id, Name: "f.txt", Size: int64(len(body)), SHA256: sum(body)}
}

var someID = strings.Repeat("b", 26)

func pullFrom(t *testing.T, r *rig, addr string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	_, _, err := r.cl.Pull(ctxT(t), addr, r.server.id.ID(), someID, dir)
	if l := leftovers(t, dir); err != nil && len(l) != 0 {
		t.Errorf("failed pull left %v", l)
	}
	return dir, err
}

func TestPullDetectsWrongContent(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		e := goodEntry(req.ID, []byte("the real thing"))
		sendFile(c, e, []byte("a forged thing"))
	})
	if _, err := pullFrom(t, r, addr); !errors.Is(err, ErrBadFile) {
		t.Errorf("got %v", err)
	}
}

func TestPullDetectsShortFile(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		body := []byte("0123456789")
		sendFile(c, goodEntry(req.ID, body), body[:4])
	})
	if _, err := pullFrom(t, r, addr); err == nil {
		t.Error("short file accepted")
	}
}

func TestPullDetectsExtraData(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		body := []byte("abc")
		sendFile(c, goodEntry(req.ID, body), append(body, "tail"...))
	})
	if _, err := pullFrom(t, r, addr); !errors.Is(err, ErrBadFile) {
		t.Errorf("got %v", err)
	}
}

func TestPullRejectsMismatchedID(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		body := []byte("abc")
		sendFile(c, goodEntry(strings.Repeat("c", 26), body), body)
	})
	if _, err := pullFrom(t, r, addr); !errors.Is(err, ErrBadResponse) {
		t.Errorf("got %v", err)
	}
}

func TestPullRejectsInvalidDescriptions(t *testing.T) {
	r := newRig(t)
	body := []byte("abc")
	bad := map[string]func(*shelf.Entry){
		"negative size": func(e *shelf.Entry) { e.Size = -1 },
		"short hash":    func(e *shelf.Entry) { e.SHA256 = "abcd" },
		"upper hash":    func(e *shelf.Entry) { e.SHA256 = strings.ToUpper(e.SHA256) },
		"non hex hash":  func(e *shelf.Entry) { e.SHA256 = strings.Repeat("z", 64) },
	}
	for name, mutate := range bad {
		addr := fake(t, r, func(c net.Conn, req request) {
			e := goodEntry(req.ID, body)
			mutate(&e)
			sendFile(c, e, body)
		})
		if _, err := pullFrom(t, r, addr); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestPullMissingDescription(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		c.Write([]byte(`{"ok":true}` + "\n"))
	})
	if _, err := pullFrom(t, r, addr); !errors.Is(err, ErrBadResponse) {
		t.Errorf("got %v", err)
	}
}

func TestHostileFileNameStaysInsideTheFolder(t *testing.T) {
	r := newRig(t)
	body := []byte("abc")
	addr := fake(t, r, func(c net.Conn, req request) {
		e := goodEntry(req.ID, body)
		e.Name = "../../../evil\x00.txt"
		sendFile(c, e, body)
	})
	parent := t.TempDir()
	dir := filepath.Join(parent, "in")
	path, e, err := r.cl.Pull(ctxT(t), addr, r.server.id.ID(), someID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || filepath.Base(path) != "evil.txt" || e.Name != "evil.txt" {
		t.Errorf("path %s name %q", path, e.Name)
	}
	if l := leftovers(t, parent); len(l) != 1 {
		t.Errorf("parent has %v", l)
	}
}

func TestGarbageAndOversizedResponses(t *testing.T) {
	r := newRig(t)
	cases := map[string]func(c net.Conn){
		"not json":  func(c net.Conn) { c.Write([]byte("hello\n")) },
		"huge line": func(c net.Conn) { c.Write(bytes.Repeat([]byte("x"), 4<<20+10)) },
		"hangs up":  func(c net.Conn) {},
	}
	for name, reply := range cases {
		addr := fake(t, r, func(c net.Conn, req request) { reply(c) })
		_, err := r.cl.List(ctxT(t), addr, r.server.id.ID())
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if name != "hangs up" && !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v, want ErrBadResponse", name, err)
		}
	}
}

func TestRemoteErrorsAreCleaned(t *testing.T) {
	r := newRig(t)
	addr := fake(t, r, func(c net.Conn, req request) {
		writeResponse(c, response{Code: "internal", Error: "boom\x1b[31m\n" + strings.Repeat("x", 500)})
	})
	_, err := r.cl.List(ctxT(t), addr, r.server.id.ID())
	var re *RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("got %v", err)
	}
	if strings.ContainsAny(re.Message, "\x1b\n") || len(re.Message) > 200 {
		t.Errorf("message %q", re.Message)
	}
}

func TestListDropsInvalidEntries(t *testing.T) {
	r := newRig(t)
	good := goodEntry(someID, []byte("x"))
	good.Name = "../x"
	badID := goodEntry("nope", []byte("x"))
	badHash := goodEntry(strings.Repeat("c", 26), []byte("x"))
	badHash.SHA256 = "zz"
	neg := goodEntry(strings.Repeat("d", 26), []byte("x"))
	neg.Size = -5
	addr := fake(t, r, func(c net.Conn, req request) {
		writeResponse(c, response{OK: true, Entries: []shelf.Entry{good, badID, badHash, neg}})
	})
	got, err := r.cl.List(ctxT(t), addr, r.server.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != someID || got[0].Name != "x" {
		t.Errorf("got %+v", got)
	}
}

func TestListCapsTheNumberOfEntries(t *testing.T) {
	r := newRig(t)
	var entries []shelf.Entry
	for i := 0; i < maxListEntries+50; i++ {
		entries = append(entries, goodEntry(someID, []byte("x")))
	}
	addr := fake(t, r, func(c net.Conn, req request) {
		writeResponse(c, response{OK: true, Entries: entries})
	})
	got, err := r.cl.List(ctxT(t), addr, r.server.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxListEntries {
		t.Errorf("got %d", len(got))
	}
}

func TestCancelStopsAStalledPull(t *testing.T) {
	r := newRig(t)
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	addr := fake(t, r, func(c net.Conn, req request) {
		body := []byte("0123456789")
		b, _ := json.Marshal(response{OK: true, Entry: ptr(goodEntry(req.ID, body))})
		c.Write(append(b, '\n'))
		c.Write(body[:3])
		<-stall
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	dir := t.TempDir()
	start := time.Now()
	_, _, err := r.cl.Pull(ctx, addr, r.server.id.ID(), someID, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancel was slow")
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftovers %v", l)
	}
}

func ptr[T any](v T) *T { return &v }

func TestIdleTimeoutStopsAStalledPull(t *testing.T) {
	r := newRig(t)
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	addr := fake(t, r, func(c net.Conn, req request) {
		body := []byte("0123456789")
		b, _ := json.Marshal(response{OK: true, Entry: ptr(goodEntry(req.ID, body))})
		c.Write(append(b, '\n'))
		<-stall
	})
	r.cl.idle = 300 * time.Millisecond
	start := time.Now()
	_, _, err := r.cl.Pull(context.Background(), addr, r.server.id.ID(), someID, t.TempDir())
	if err == nil {
		t.Error("expected a timeout")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout was slow")
	}
}

func TestServerDropsIdleClients(t *testing.T) {
	r := newRigWith(t, func(s *Server) { s.idle = 300 * time.Millisecond })
	cfg := identity.PinnedConfig(r.client.cert, func(id identity.DeviceID) bool { return id == r.server.id.ID() })
	conn, err := tls.Dial("tcp", r.addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("read succeeded")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("server kept an idle client too long")
	}
}

func TestFailedConnectionsAreLoggedNotFatal(t *testing.T) {
	r := newRig(t)
	stranger := &Client{Cert: newDevice(t).cert}
	stranger.List(ctxT(t), r.addr, r.server.id.ID())
	// the server still works for the paired device
	if _, err := r.cl.List(ctxT(t), r.addr, r.server.id.ID()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	if len(r.logs.b) == 0 {
		t.Error("rejected stranger was not logged")
	}
}

func TestReadLine(t *testing.T) {
	line, err := readLine(bufio.NewReader(strings.NewReader("abc\nrest")), 10)
	if err != nil || string(line) != "abc" {
		t.Errorf("%q %v", line, err)
	}
	if _, err := readLine(bufio.NewReader(strings.NewReader("abcdef\n")), 5); !errors.Is(err, ErrBadResponse) {
		t.Errorf("too long: %v", err)
	}
	if _, err := readLine(bufio.NewReader(strings.NewReader("abc")), 10); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("no newline: %v", err)
	}
	if _, err := readLine(bufio.NewReader(strings.NewReader("")), 10); !errors.Is(err, io.EOF) {
		t.Errorf("empty: %v", err)
	}
	if line, err := readLine(bufio.NewReader(strings.NewReader("12345\n")), 5); err != nil || string(line) != "12345" {
		t.Errorf("exact length: %q %v", line, err)
	}
}

func TestPokeReachesTheServerWithTheCallersID(t *testing.T) {
	got := make(chan identity.DeviceID, 4)
	r := newRigWith(t, func(s *Server) { s.OnPoke = func(id identity.DeviceID) { got <- id } })
	if err := r.cl.Poke(ctxT(t), r.addr, r.server.id.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-got:
		if id != r.client.id.ID() {
			t.Errorf("poke from %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnPoke was not called")
	}
}

func TestPokeWithoutACallbackIsHarmless(t *testing.T) {
	r := newRig(t)
	if err := r.cl.Poke(ctxT(t), r.addr, r.server.id.ID()); err != nil {
		t.Fatal(err)
	}
}

func TestStrangersCannotPoke(t *testing.T) {
	called := make(chan struct{}, 1)
	r := newRigWith(t, func(s *Server) { s.OnPoke = func(identity.DeviceID) { called <- struct{}{} } })
	stranger := &Client{Cert: newDevice(t).cert}
	if err := stranger.Poke(ctxT(t), r.addr, r.server.id.ID()); err == nil {
		t.Error("stranger could poke")
	}
	select {
	case <-called:
		t.Error("OnPoke ran for a stranger")
	case <-time.After(200 * time.Millisecond):
	}
}
