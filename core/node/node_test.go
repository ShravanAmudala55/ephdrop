package node

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

// hub is an in-memory stand-in for the network's multicast DNS.
type hub struct {
	mu    sync.Mutex
	ports map[identity.DeviceID]int
}

func newHub() *hub { return &hub{ports: map[identity.DeviceID]int{}} }

type hubBackend struct{ h *hub }

func (b hubBackend) Advertise(ctx context.Context, ad discovery.Advertisement) error {
	b.h.mu.Lock()
	b.h.ports[ad.ID] = ad.Port
	b.h.mu.Unlock()
	<-ctx.Done()
	b.h.mu.Lock()
	delete(b.h.ports, ad.ID)
	b.h.mu.Unlock()
	return ctx.Err()
}

func (b hubBackend) Browse(ctx context.Context, onSeen func(discovery.Sighting), _ func(identity.DeviceID)) error {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		b.h.mu.Lock()
		for id, p := range b.h.ports {
			onSeen(discovery.Sighting{ID: id, Addrs: []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(p))}})
		}
		b.h.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func newNode(t *testing.T, h *hub, name string) *Node {
	t.Helper()
	n, err := New(Config{
		Dir: t.TempDir(), Name: name, Listen: "127.0.0.1:0",
		Backend: hubBackend{h}, RefreshEvery: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func start(t *testing.T, n *Node) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() { n.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("node did not stop")
		}
	})
}

// pair pairs a and b: a makes the invite, b joins, and a accepts or declines.
func pair(t *testing.T, a, b *Node, accept bool) (PairResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := a.StartInvite(ctx)
	if err != nil {
		t.Skipf("cannot make an invite here: %v", err)
	}
	go func() {
		select {
		case req := <-sess.Requests:
			if req.Peer.ID != b.ID() || req.Peer.Name != b.Name() {
				t.Errorf("request from %+v", req.Peer)
			}
			req.Reply(accept)
		case <-ctx.Done():
		}
	}()
	_, joinErr := b.Join(ctx, sess.Text)
	res := <-sess.Done
	return res, joinErr
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewNeedsADirectory(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("expected an error")
	}
}

func TestIdentityIsKeptBetweenRuns(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Config{Dir: dir, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Dir: dir, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != b.ID() {
		t.Error("id changed")
	}
	if a.DownloadDir() != filepath.Join(dir, "downloads") {
		t.Errorf("download dir %s", a.DownloadDir())
	}
}

func TestStartTwiceAndStop(t *testing.T) {
	n := newNode(t, newHub(), "a")
	if n.Port() != 0 {
		t.Error("port before start")
	}
	start(t, n)
	if n.Port() == 0 {
		t.Error("no port")
	}
	if err := n.Start(context.Background()); err == nil {
		t.Error("second Start accepted")
	}
}

func TestFetchBeforeStart(t *testing.T) {
	n := newNode(t, newHub(), "a")
	if _, _, err := n.Fetch(context.Background(), "x", strings.Repeat("a", 26), ""); !errors.Is(err, ErrNotRunning) {
		t.Errorf("got %v", err)
	}
}

func TestShareListRemove(t *testing.T) {
	n := newNode(t, newHub(), "a")
	ch, cancel := n.Subscribe()
	defer cancel()
	e, err := n.Share(strings.NewReader("data"), "../x.txt", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "x.txt" {
		t.Errorf("name %q", e.Name)
	}
	select {
	case <-ch:
	default:
		t.Error("no notification after sharing")
	}
	items := n.Items()
	if len(items) != 1 || !items[0].Local || items[0].ID != e.ID {
		t.Fatalf("items %+v", items)
	}
	f, _, err := n.Local(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "data" {
		t.Errorf("content %q", b)
	}
	if err := n.Remove(e.ID); err != nil {
		t.Fatal(err)
	}
	if len(n.Items()) != 0 {
		t.Error("still listed")
	}
	if err := n.Remove(e.ID); err == nil {
		t.Error("second remove accepted")
	}
}

func TestShareFile(t *testing.T) {
	n := newNode(t, newHub(), "a")
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.txt")
	os.WriteFile(p, []byte("hello"), 0o600)
	e, err := n.ShareFile(p, 0)
	if err != nil || e.Name != "doc.txt" || e.Size != 5 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := n.ShareFile(dir, 0); err == nil {
		t.Error("a folder was accepted")
	}
	if _, err := n.ShareFile(filepath.Join(dir, "missing"), 0); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestPairShareAndFetch(t *testing.T) {
	h := newHub()
	a, b := newNode(t, h, "laptop"), newNode(t, h, "phone")
	start(t, a)
	start(t, b)

	res, joinErr := pair(t, a, b, true)
	if joinErr != nil || res.Err != nil {
		t.Fatalf("join %v, invite %v", joinErr, res.Err)
	}
	if res.Peer.ID != b.ID() {
		t.Errorf("peer %+v", res.Peer)
	}
	for _, n := range []*Node{a, b} {
		if len(n.Peers()) != 1 {
			t.Fatalf("%s has %d peers", n.Name(), len(n.Peers()))
		}
	}
	waitFor(t, func() bool { return a.Peers()[0].Online && b.Peers()[0].Online }, "both online")

	// A shares, B sees it and pulls it.
	bch, cancel := b.Subscribe()
	defer cancel()
	e, err := a.Share(strings.NewReader("over the wire"), "note.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, it := range b.Items() {
			if it.ID == e.ID && it.Reachable && !it.Local {
				return true
			}
		}
		return false
	}, "B to see A's file")
	select {
	case <-bch:
	case <-time.After(5 * time.Second):
		t.Error("B was not notified")
	}
	dir := t.TempDir()
	path, _, err := b.Fetch(context.Background(), a.ID(), e.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "over the wire" {
		t.Errorf("content %q", got)
	}

	// The other direction works too, into the default download folder.
	e2, _ := b.Share(strings.NewReader("and back"), "reply.txt", 0)
	waitFor(t, func() bool {
		for _, it := range a.Items() {
			if it.ID == e2.ID && it.Reachable {
				return true
			}
		}
		return false
	}, "A to see B's file")
	path2, _, err := a.Fetch(context.Background(), b.ID(), e2.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path2) != a.DownloadDir() {
		t.Errorf("saved in %s", path2)
	}

	// Fetching your own file is refused.
	if _, _, err := a.Fetch(context.Background(), a.ID(), e.ID, dir); err == nil {
		t.Error("fetched own file")
	}
}

func TestDecliningLeavesNobodyPaired(t *testing.T) {
	h := newHub()
	a, b := newNode(t, h, "a"), newNode(t, h, "b")
	res, joinErr := pair(t, a, b, false)
	if joinErr == nil || res.Err == nil {
		t.Errorf("join %v, invite %v", joinErr, res.Err)
	}
	if len(a.Peers()) != 0 || len(b.Peers()) != 0 {
		t.Error("devices paired anyway")
	}
}

func TestCancellingAnInvite(t *testing.T) {
	a := newNode(t, newHub(), "a")
	sess, err := a.StartInvite(context.Background())
	if err != nil {
		t.Skipf("cannot make an invite here: %v", err)
	}
	sess.Cancel()
	select {
	case res := <-sess.Done:
		if res.Err == nil {
			t.Error("cancelled invite reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done never arrived")
	}
	if sess.Expires.Before(time.Now().Add(4 * time.Minute)) {
		t.Errorf("expiry %v", sess.Expires)
	}
}

func TestJoinWithABadInvite(t *testing.T) {
	a := newNode(t, newHub(), "a")
	if _, err := a.Join(context.Background(), "not an invite"); err == nil {
		t.Error("expected an error")
	}
}

func TestUnpairing(t *testing.T) {
	h := newHub()
	a, b := newNode(t, h, "a"), newNode(t, h, "b")
	start(t, a)
	start(t, b)
	if res, err := pair(t, a, b, true); err != nil || res.Err != nil {
		t.Fatal(err, res.Err)
	}
	e, _ := a.Share(strings.NewReader("x"), "f", 0)
	waitFor(t, func() bool { return len(b.Items()) == 1 }, "B to see the file")

	ch, cancel := b.Subscribe()
	defer cancel()
	if err := b.Unpair(a.ID()); err != nil {
		t.Fatal(err)
	}
	if len(b.Items()) != 0 || len(b.Peers()) != 0 {
		t.Error("B still shows A")
	}
	select {
	case <-ch:
	default:
		t.Error("no notification")
	}
	// B is no longer allowed to fetch. A still thinks B is paired, but B will
	// not even try, and A would reject B's key once A unpairs too.
	if _, _, err := b.Fetch(context.Background(), a.ID(), e.ID, t.TempDir()); err == nil {
		t.Error("fetched from an unpaired device")
	}
	if err := b.Unpair(a.ID()); err != nil {
		t.Errorf("unpairing twice should be harmless: %v", err)
	}
	// Once A unpairs too, B cannot reach A's server at all.
	a.Unpair(b.ID())
	if _, _, err := b.Fetch(context.Background(), a.ID(), e.ID, t.TempDir()); err == nil {
		t.Error("fetch worked after both unpaired")
	}
}

func TestExpiredFilesAreSweptWhileRunning(t *testing.T) {
	n, err := New(Config{Dir: t.TempDir(), Listen: "127.0.0.1:0", Backend: hubBackend{newHub()}, SweepEvery: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start(t, n)
	e, err := n.Share(strings.NewReader("x"), "short", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := n.Subscribe()
	defer cancel()
	for len(ch) > 0 {
		<-ch
	}
	waitFor(t, func() bool { _, _, err := n.Local(e.ID); return err != nil }, "the file to be deleted")
}

func TestPeersListsOfflineDevices(t *testing.T) {
	h := newHub()
	a, b := newNode(t, h, "a"), newNode(t, h, "b")
	start(t, a) // b is not started, so it is paired but never announced
	if res, err := pair(t, a, b, true); err != nil || res.Err != nil {
		t.Fatal(err, res.Err)
	}
	ps := a.Peers()
	if len(ps) != 1 || ps[0].Online {
		t.Errorf("peers %+v", ps)
	}
}

func TestSubscribeMergesAndStops(t *testing.T) {
	n := newNode(t, newHub(), "a")
	ch, cancel := n.Subscribe()
	for i := 0; i < 3; i++ {
		n.Share(strings.NewReader("x"), "f", 0)
	}
	if len(ch) != 1 {
		t.Errorf("queued %d", len(ch))
	}
	<-ch
	cancel()
	cancel() // safe twice
	n.Share(strings.NewReader("x"), "f", 0)
	if len(ch) != 0 {
		t.Error("notified after cancel")
	}
}

func TestNewFilesShowUpOnOtherDevicesAtOnceNotAtTheNextRefresh(t *testing.T) {
	h := newHub()
	mk := func(name string) *Node {
		n, err := New(Config{Dir: t.TempDir(), Name: name, Listen: "127.0.0.1:0", Backend: hubBackend{h}, RefreshEvery: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, b := mk("a"), mk("b")
	start(t, a)
	start(t, b)
	if res, err := pair(t, a, b, true); err != nil || res.Err != nil {
		t.Fatal(err, res.Err)
	}
	waitFor(t, func() bool { return a.Peers()[0].Online && b.Peers()[0].Online }, "both online")
	time.Sleep(300 * time.Millisecond) // let the first lists settle

	start := time.Now()
	e, _ := a.Share(strings.NewReader("hello"), "fresh.txt", 0)
	waitFor(t, func() bool {
		for _, it := range b.Items() {
			if it.ID == e.ID {
				return true
			}
		}
		return false
	}, "B to see the new file")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}

	// and removing it disappears just as fast
	a.Remove(e.ID)
	waitFor(t, func() bool { return len(b.Items()) == 0 }, "B to see the removal")
}
