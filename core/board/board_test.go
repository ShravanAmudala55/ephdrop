package board

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
	"github.com/ShravanAmudala55/ephdrop/core/transfer"
)

const (
	selfID = identity.DeviceID("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	peerA  = identity.DeviceID("bbbbbbbbbbbbbbbbbbbbbbbbbb")
	peerB  = identity.DeviceID("cccccccccccccccccccccccccc")
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func fid(c string) string { return strings.Repeat(c, 26) }

func entry(id string, created time.Time, ttl time.Duration) shelf.Entry {
	return shelf.Entry{ID: id, Name: "f-" + id[:1], Size: 3, SHA256: strings.Repeat("0", 64), Created: created, Expires: created.Add(ttl)}
}

// fakeRemote answers from maps keyed by address.
type fakeRemote struct {
	mu      sync.Mutex
	lists   map[string][]shelf.Entry
	listErr map[string]error
	pullErr map[string]error
	listN   map[string]int
	pulls   []string
	block   chan struct{} // if set, List waits for it
	onList  func()        // if set, called when List starts
}

func newFake() *fakeRemote {
	return &fakeRemote{lists: map[string][]shelf.Entry{}, listErr: map[string]error{}, pullErr: map[string]error{}, listN: map[string]int{}}
}

func (f *fakeRemote) List(ctx context.Context, addr string, peer identity.DeviceID) ([]shelf.Entry, error) {
	f.mu.Lock()
	f.listN[addr]++
	block, onList := f.block, f.onList
	f.mu.Unlock()
	if onList != nil {
		onList()
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[addr]; err != nil {
		return nil, err
	}
	return append([]shelf.Entry(nil), f.lists[addr]...), nil
}

func (f *fakeRemote) Pull(ctx context.Context, addr string, peer identity.DeviceID, id, dir string) (string, shelf.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, addr+"|"+id+"|"+dir)
	if err := f.pullErr[addr]; err != nil {
		return "", shelf.Entry{}, err
	}
	return dir + "/" + id, shelf.Entry{ID: id}, nil
}

func (f *fakeRemote) set(addr string, es ...shelf.Entry) {
	f.mu.Lock()
	f.lists[addr] = es
	f.mu.Unlock()
}

type env struct {
	b      *Board
	finder *discovery.Finder
	fake   *fakeRemote
	now    time.Time
	nowMu  sync.Mutex
	allow  map[identity.DeviceID]bool
	amu    sync.Mutex
}

func newEnv(t *testing.T, local *shelf.Shelf) *env {
	t.Helper()
	e := &env{fake: newFake(), now: t0, allow: map[identity.DeviceID]bool{peerA: true, peerB: true}}
	allow := func(id identity.DeviceID) bool {
		e.amu.Lock()
		defer e.amu.Unlock()
		return e.allow[id]
	}
	e.finder = discovery.NewFinder(selfID, allow, time.Hour)
	e.b = New(selfID, local, e.finder, e.fake, allow)
	e.b.now = func() time.Time {
		e.nowMu.Lock()
		defer e.nowMu.Unlock()
		return e.now
	}
	return e
}

func (e *env) advance(d time.Duration) {
	e.nowMu.Lock()
	e.now = e.now.Add(d)
	e.nowMu.Unlock()
}

func (e *env) see(id identity.DeviceID, addrs ...string) {
	e.finder.Seen(discovery.Sighting{ID: id, Addrs: addrs})
}

func ids(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Holder)[:1]+":"+it.ID[:1])
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestItemsMergeLocalAndRemoteNewestFirst(t *testing.T) {
	dir := t.TempDir()
	sh, _ := shelf.Open(dir, selfID)
	local, err := sh.Add(strings.NewReader("x"), "mine", time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, sh)
	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now().Add(-time.Hour), 5*time.Hour), entry(fid("e"), time.Now().Add(-3*time.Hour), 5*time.Hour))
	e.fake.set("127.0.0.1:2", entry(fid("f"), time.Now().Add(-2*time.Hour), 5*time.Hour))
	e.b.now = time.Now
	e.see(peerA, "127.0.0.1:1")
	e.see(peerB, "127.0.0.1:2")
	for _, p := range []identity.DeviceID{peerA, peerB} {
		if err := e.b.Refresh(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	items := e.b.Items()
	want := []string{"a:" + local.ID[:1], "b:d", "c:f", "b:e"}
	if !eq(ids(items), want) {
		t.Fatalf("got %v, want %v", ids(items), want)
	}
	if !items[0].Local || items[1].Local {
		t.Error("Local flag wrong")
	}
	for _, it := range items {
		if !it.Reachable {
			t.Errorf("%v should be reachable", it.ID)
		}
	}
}

func TestNilShelfIsFine(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	if got := e.b.Items(); len(got) != 1 {
		t.Errorf("got %d", len(got))
	}
}

func TestExpiredFilesAreHidden(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour), entry(fid("e"), t0, 3*time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	if len(e.b.Items()) != 2 {
		t.Fatal("setup")
	}
	e.advance(2 * time.Hour)
	got := e.b.Items()
	if len(got) != 1 || got[0].ID != fid("e") {
		t.Errorf("got %v", ids(got))
	}
	e.advance(time.Hour) // exactly at expiry
	if len(e.b.Items()) != 0 {
		t.Error("file at its expiry time still shown")
	}
}

func TestUnreachableDeviceKeepsItsFilesButCannotBePulled(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	e.finder.Forget(peerA) // no longer visible
	items := e.b.Items()
	if len(items) != 1 || items[0].Reachable {
		t.Fatalf("items %+v", items)
	}
	_, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out")
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v", err)
	}
	if len(e.fake.pulls) != 0 {
		t.Error("tried to pull from an unreachable device")
	}
}

func TestFailedRefreshKeepsOldList(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	if err := e.b.Refresh(context.Background(), peerA); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	e.fake.listErr["127.0.0.1:1"] = errors.New("network down")
	e.fake.mu.Unlock()
	if err := e.b.Refresh(context.Background(), peerA); err == nil {
		t.Fatal("expected an error")
	}
	if len(e.b.Items()) != 1 {
		t.Error("old list lost")
	}
	if e.b.LastError(peerA) == nil {
		t.Error("error not recorded")
	}
	e.fake.mu.Lock()
	delete(e.fake.listErr, "127.0.0.1:1")
	e.fake.mu.Unlock()
	if err := e.b.Refresh(context.Background(), peerA); err != nil {
		t.Fatal(err)
	}
	if e.b.LastError(peerA) != nil {
		t.Error("error not cleared")
	}
}

func TestRefreshTriesEachAddress(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.listErr["127.0.0.1:1"] = errors.New("refused")
	e.fake.set("127.0.0.1:2", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1", "127.0.0.1:2")
	if err := e.b.Refresh(context.Background(), peerA); err != nil {
		t.Fatal(err)
	}
	if len(e.b.Items()) != 1 {
		t.Error("second address not used")
	}
}

func TestRefreshOfInvisibleDevice(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.b.Refresh(context.Background(), peerA); !errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v", err)
	}
}

func TestConcurrentRefreshesOfOneDeviceAreMerged(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.block = make(chan struct{})
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.b.Refresh(context.Background(), peerA) }()
	}
	time.Sleep(200 * time.Millisecond)
	close(e.fake.block)
	wg.Wait()
	e.fake.mu.Lock()
	n := e.fake.listN["127.0.0.1:1"]
	e.fake.mu.Unlock()
	if n != 1 {
		t.Errorf("List called %d times", n)
	}
}

func TestUnpairedDevicesAreHiddenAndRefused(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	e.amu.Lock()
	e.allow[peerA] = false
	e.amu.Unlock()
	if len(e.b.Items()) != 0 {
		t.Error("unpaired device's files shown")
	}
	if _, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out"); !errors.Is(err, ErrUnknownFile) {
		t.Errorf("pulled from an unpaired device: %v", err)
	}
	if len(e.fake.pulls) != 0 {
		t.Error("contacted an unpaired device")
	}
}

func TestPullChecksTheFileIsKnown(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	if _, _, err := e.b.Pull(context.Background(), peerA, fid("z"), "out"); !errors.Is(err, ErrUnknownFile) {
		t.Errorf("unknown id: %v", err)
	}
	if _, _, err := e.b.Pull(context.Background(), peerB, fid("d"), "out"); !errors.Is(err, ErrUnknownFile) {
		t.Errorf("wrong holder: %v", err)
	}
	e.advance(2 * time.Hour)
	if _, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out"); !errors.Is(err, ErrUnknownFile) {
		t.Errorf("expired: %v", err)
	}
}

func TestPullUsesTheRightArguments(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	path, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "downloads")
	if err != nil || path != "downloads/"+fid("d") {
		t.Fatalf("%q %v", path, err)
	}
	if len(e.fake.pulls) != 1 || e.fake.pulls[0] != "127.0.0.1:1|"+fid("d")+"|downloads" {
		t.Errorf("pulls %v", e.fake.pulls)
	}
}

func TestPullFallsBackToTheNextAddress(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.fake.pullErr["127.0.0.1:1"] = errors.New("connection refused")
	e.see(peerA, "127.0.0.1:1", "127.0.0.1:2")
	e.b.Refresh(context.Background(), peerA)
	if _, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out"); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.pulls) != 2 {
		t.Errorf("pulls %v", e.fake.pulls)
	}
}

func TestPullStopsWhenTheFileIsGone(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.fake.pullErr["127.0.0.1:1"] = shelf.ErrNotFound
	e.see(peerA, "127.0.0.1:1", "127.0.0.1:2")
	e.b.Refresh(context.Background(), peerA)
	_, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out")
	if !errors.Is(err, shelf.ErrNotFound) {
		t.Errorf("got %v", err)
	}
	if len(e.fake.pulls) != 1 {
		t.Errorf("kept trying: %v", e.fake.pulls)
	}
}

func TestPullFailureIsReported(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.fake.pullErr["127.0.0.1:1"] = errors.New("boom")
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	if _, _, err := e.b.Pull(context.Background(), peerA, fid("d"), "out"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("got %v", err)
	}
}

func TestForgetRemovesFiles(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	ch, cancel := e.b.Subscribe()
	defer cancel()
	e.b.Forget(peerA)
	if len(e.b.Items()) != 0 {
		t.Error("files remain")
	}
	select {
	case <-ch:
	default:
		t.Error("no notification")
	}
	e.b.Forget(peerA) // again: nothing to say
	select {
	case <-ch:
		t.Error("notified for nothing")
	default:
	}
}

func TestSubscribeNotifiesOnlyOnChange(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	ch, cancel := e.b.Subscribe()
	e.b.Refresh(context.Background(), peerA)
	select {
	case <-ch:
	default:
		t.Fatal("no notification for the first list")
	}
	e.b.Refresh(context.Background(), peerA) // same list again
	select {
	case <-ch:
		t.Error("notified although nothing changed")
	default:
	}
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour), entry(fid("e"), t0, time.Hour))
	e.b.Refresh(context.Background(), peerA)
	e.b.Refresh(context.Background(), peerA)
	select {
	case <-ch:
	default:
		t.Error("no notification for a changed list")
	}
	select {
	case <-ch:
		t.Error("notifications were not merged")
	default:
	}
	cancel()
	e.fake.set("127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	select {
	case <-ch:
		t.Error("notified after unsubscribing")
	default:
	}
}

func TestRunRefreshesNewAndChangedDevices(t *testing.T) {
	e := newEnv(t, nil)
	e.b.RefreshEvery = 50 * time.Millisecond
	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now(), time.Hour))
	e.b.now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.b.Run(ctx); close(done) }()
	ch, unsub := e.b.Subscribe()
	defer unsub()

	e.see(peerA, "127.0.0.1:1") // appears while running
	waitFor(t, func() bool { return len(e.b.Items()) == 1 }, "first list")
	_ = ch

	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now(), time.Hour), entry(fid("e"), time.Now(), time.Hour))
	waitFor(t, func() bool { return len(e.b.Items()) == 2 }, "periodic refresh")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunRefreshesDevicesThatWereAlreadyVisible(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now(), time.Hour))
	e.b.now = time.Now
	e.see(peerA, "127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.b.Run(ctx)
	waitFor(t, func() bool { return len(e.b.Items()) == 1 }, "initial refresh")
}

func TestRunNotifiesWhenADeviceDisappears(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now(), time.Hour))
	e.b.now = time.Now
	e.see(peerA, "127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.b.Run(ctx)
	waitFor(t, func() bool { return len(e.b.Items()) == 1 }, "list")
	ch, unsub := e.b.Subscribe()
	defer unsub()
	for len(ch) > 0 {
		<-ch
	}
	e.finder.Forget(peerA)
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no notification")
	}
	if items := e.b.Items(); len(items) != 1 || items[0].Reachable {
		t.Errorf("items %+v", items)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunStopsWhileRefreshesAreBlocked(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.block = make(chan struct{})
	e.see(peerA, "127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.b.Run(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// ---- with the real transfer layer

func TestEndToEndWithRealTransfer(t *testing.T) {
	mk := func() (*identity.Identity, tls.Certificate) {
		id, err := identity.Generate()
		if err != nil {
			t.Fatal(err)
		}
		c, err := id.TLSCertificate()
		if err != nil {
			t.Fatal(err)
		}
		return id, c
	}
	serverID, serverCert := mk()
	clientID, clientCert := mk()

	sh, err := shelf.Open(t.TempDir(), serverID.ID())
	if err != nil {
		t.Fatal(err)
	}
	file, err := sh.Add(strings.NewReader("shared across the network"), "hello.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := &transfer.Server{
		Cert: serverCert, Shelf: sh,
		Allow: func(id identity.DeviceID) bool { return id == clientID.ID() },
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	sdone := make(chan struct{})
	go func() { srv.Serve(sctx, ln); close(sdone) }()
	defer func() { scancel(); <-sdone }()

	allow := func(id identity.DeviceID) bool { return id == serverID.ID() }
	finder := discovery.NewFinder(clientID.ID(), allow, time.Hour)
	b := New(clientID.ID(), nil, finder, &transfer.Client{Cert: clientCert}, allow)
	b.RefreshEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	finder.Seen(discovery.Sighting{ID: serverID.ID(), Addrs: []string{ln.Addr().String()}})
	waitFor(t, func() bool { return len(b.Items()) == 1 }, "remote list")
	it := b.Items()[0]
	if it.ID != file.ID || it.Holder != serverID.ID() || !it.Reachable || it.Local {
		t.Fatalf("item %+v", it)
	}
	dir := t.TempDir()
	path, _, err := b.Pull(context.Background(), it.Holder, it.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "shared across the network" {
		t.Errorf("content %q", got)
	}
}

func TestRefreshDiscardsAnAnswerFromADeviceUnpairedMeanwhile(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.fake.onList = func() {
		e.amu.Lock()
		e.allow[peerA] = false
		e.amu.Unlock()
	}
	e.see(peerA, "127.0.0.1:1")
	if err := e.b.Refresh(context.Background(), peerA); !errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v", err)
	}
	e.amu.Lock()
	e.allow[peerA] = true
	e.amu.Unlock()
	if len(e.b.Items()) != 0 {
		t.Error("answer from an unpaired device was kept")
	}
}

func TestSameFileFromTwoHoldersListsBoth(t *testing.T) {
	e := newEnv(t, nil)
	same := entry(fid("d"), t0, time.Hour)
	e.fake.set("127.0.0.1:1", same)
	e.fake.set("127.0.0.1:2", same)
	e.see(peerA, "127.0.0.1:1")
	e.see(peerB, "127.0.0.1:2")
	e.b.Refresh(context.Background(), peerA)
	e.b.Refresh(context.Background(), peerB)
	if got := ids(e.b.Items()); !eq(got, []string{"b:d", "c:d"}) {
		t.Errorf("got %v", got)
	}
}

func TestChangedEntryWithSameIDIsNoticed(t *testing.T) {
	e := newEnv(t, nil)
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, time.Hour))
	e.see(peerA, "127.0.0.1:1")
	e.b.Refresh(context.Background(), peerA)
	ch, cancel := e.b.Subscribe()
	defer cancel()
	e.fake.set("127.0.0.1:1", entry(fid("d"), t0, 2*time.Hour))
	e.b.Refresh(context.Background(), peerA)
	select {
	case <-ch:
	default:
		t.Error("changed expiry not noticed")
	}
}

func TestNewDeviceIsPickedUpFromTheEventNotJustTheTimer(t *testing.T) {
	e := newEnv(t, nil)
	e.b.RefreshEvery = time.Hour
	e.fake.set("127.0.0.1:1", entry(fid("d"), time.Now(), time.Hour))
	e.b.now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.b.Run(ctx)
	time.Sleep(100 * time.Millisecond) // Run has subscribed and found nobody
	e.see(peerA, "127.0.0.1:1")
	waitFor(t, func() bool { return len(e.b.Items()) == 1 }, "list after the device appeared")
}

func TestDefaultRefreshInterval(t *testing.T) {
	e := newEnv(t, nil)
	if e.b.refreshEvery() != DefaultRefresh {
		t.Errorf("got %v", e.b.refreshEvery())
	}
	e.b.RefreshEvery = time.Second
	if e.b.refreshEvery() != time.Second {
		t.Errorf("got %v", e.b.refreshEvery())
	}
}
