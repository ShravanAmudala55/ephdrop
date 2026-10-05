package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type allowSet struct {
	mu sync.Mutex
	m  map[identity.DeviceID]bool
}

func (a *allowSet) Allow(id identity.DeviceID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[id]
}

func (a *allowSet) Set(id identity.DeviceID, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[id] = ok
}

const testTTL = time.Minute

type rig struct {
	f     *Finder
	clock *fakeClock
	allow *allowSet
	self  identity.DeviceID
}

func newRig(t *testing.T, paired ...identity.DeviceID) *rig {
	t.Helper()
	r := &rig{
		clock: &fakeClock{t: time.Unix(1_700_000_000, 0)},
		allow: &allowSet{m: map[identity.DeviceID]bool{}},
		self:  newID(t),
	}
	for _, id := range paired {
		r.allow.Set(id, true)
	}
	r.f = NewFinder(r.self, r.allow.Allow, testTTL)
	r.f.now = r.clock.Now
	return r
}

func expect(t *testing.T, ch <-chan Event, kind EventKind) Event {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Kind != kind {
			t.Fatalf("got %v event, want %v", ev.Kind, kind)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("no %v event", kind)
		return Event{}
	}
}

func expectNone(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected %v event", ev.Kind)
	default:
	}
}

func TestSeenTracksPairedDevice(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.f.Seen(Sighting{ID: peer, Addrs: []string{"[fd00::5]:4000", "192.168.1.5:4000"}})

	ev := expect(t, events, Appeared)
	if ev.Peer.ID != peer {
		t.Fatalf("event for %q", ev.Peer.ID)
	}
	peers := r.f.Peers()
	if len(peers) != 1 {
		t.Fatalf("Peers = %+v", peers)
	}
	p := peers[0]
	if p.ID != peer || p.Hinted || !p.LastSeen.Equal(r.clock.Now()) {
		t.Fatalf("peer = %+v", p)
	}
	if len(p.Addrs) != 2 || p.Addrs[0] != "192.168.1.5:4000" || p.Addrs[1] != "[fd00::5]:4000" {
		t.Fatalf("addrs = %v", p.Addrs)
	}
	if got, ok := r.f.Get(peer); !ok || got.ID != peer {
		t.Fatal("Get did not find the device")
	}
	if _, ok := r.f.Get(newID(t)); ok {
		t.Fatal("Get found an unknown device")
	}
}

func TestSeenIgnoresSelfAndStrangers(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	r.allow.Set(r.self, true) // even if our own id were somehow "paired"
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.f.Seen(Sighting{ID: r.self, Addrs: []string{"192.168.1.1:1"}})
	r.f.Seen(Sighting{ID: newID(t), Addrs: []string{"192.168.1.2:1"}})

	expectNone(t, events)
	if len(r.f.Peers()) != 0 {
		t.Fatal("tracked a device that should be ignored")
	}
	// Strangers must not even use memory, or anyone on the network could grow
	// our tables by announcing made up ids.
	r.f.mu.Lock()
	held := len(r.f.entries)
	r.f.mu.Unlock()
	if held != 0 {
		t.Fatalf("holding %d entries for ignored devices", held)
	}
}

func TestSeenIgnoresUnusableAddresses(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)

	r.f.Seen(Sighting{ID: peer, Addrs: []string{"8.8.8.8:80", "host.local:80", "10.0.0.1"}})
	if len(r.f.Peers()) != 0 {
		t.Fatal("tracked a device with no usable address")
	}
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"8.8.8.8:80", "10.0.0.7:80"}})
	p, ok := r.f.Get(peer)
	if !ok || len(p.Addrs) != 1 || p.Addrs[0] != "10.0.0.7:80" {
		t.Fatalf("peer = %+v, %v", p, ok)
	}
}

func TestRepeatedSightingRefreshesWithoutAnEvent(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	s := Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}}
	r.f.Seen(s)
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.clock.Advance(10 * time.Second)
	r.f.Seen(s)

	expectNone(t, events)
	p, _ := r.f.Get(peer)
	if !p.LastSeen.Equal(r.clock.Now()) {
		t.Fatal("LastSeen was not refreshed")
	}
}

func TestAddressChangeEmitsUpdated(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.9:4000"}})

	ev := expect(t, events, Updated)
	if len(ev.Peer.Addrs) != 1 || ev.Peer.Addrs[0] != "192.168.1.9:4000" {
		t.Fatalf("addrs = %v", ev.Peer.Addrs)
	}
}

func TestQuietDeviceExpires(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	events, cancel := r.f.Subscribe()
	defer cancel()
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	expect(t, events, Appeared)

	r.clock.Advance(testTTL - time.Second)
	if len(r.f.Peers()) != 1 {
		t.Fatal("device expired too early")
	}
	r.clock.Advance(2 * time.Second)
	if len(r.f.Peers()) != 0 {
		t.Fatal("device did not expire")
	}
	ev := expect(t, events, Disappeared)
	if ev.Peer.ID != peer || len(ev.Peer.Addrs) == 0 {
		t.Fatalf("Disappeared should carry the last known state, got %+v", ev.Peer)
	}
	expectNone(t, events)
}

func TestSightingsKeepADeviceAlive(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	s := Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}}
	for i := 0; i < 5; i++ {
		r.f.Seen(s)
		r.clock.Advance(testTTL - 5*time.Second)
	}
	if len(r.f.Peers()) != 1 {
		t.Fatal("device that kept announcing expired")
	}
}

func TestGoneRemovesDevice(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.f.Gone(peer)
	expect(t, events, Disappeared)
	if len(r.f.Peers()) != 0 {
		t.Fatal("device still visible after goodbye")
	}
	r.f.Gone(peer)     // repeating is harmless
	r.f.Gone(newID(t)) // so is an unknown id
	expectNone(t, events)
}

func TestHints(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	events, cancel := r.f.Subscribe()
	defer cancel()

	if err := r.f.Hint(peer, "100.64.0.9:4000"); err != nil {
		t.Fatal(err)
	}
	ev := expect(t, events, Appeared)
	if !ev.Peer.Hinted || !ev.Peer.LastSeen.IsZero() {
		t.Fatalf("hint-only peer = %+v", ev.Peer)
	}

	// An announcement adds addresses next to the hint.
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	ev = expect(t, events, Updated)
	if len(ev.Peer.Addrs) != 2 {
		t.Fatalf("addrs = %v", ev.Peer.Addrs)
	}

	// The announcement expires, the hint stays.
	r.clock.Advance(2 * testTTL)
	p, ok := r.f.Get(peer)
	if !ok || len(p.Addrs) != 1 || p.Addrs[0] != "100.64.0.9:4000" || !p.Hinted {
		t.Fatalf("after expiry: %+v, %v", p, ok)
	}
	expect(t, events, Updated)

	// A goodbye does not remove the hint either.
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	expect(t, events, Updated)
	r.f.Gone(peer)
	expect(t, events, Updated)
	if _, ok := r.f.Get(peer); !ok {
		t.Fatal("hint was lost")
	}

	// No addresses clears the hint.
	if err := r.f.Hint(peer); err != nil {
		t.Fatal(err)
	}
	expect(t, events, Disappeared)
	if len(r.f.Peers()) != 0 {
		t.Fatal("cleared hint is still visible")
	}
}

func TestHintValidation(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)

	if err := r.f.Hint(newID(t), "10.0.0.1:80"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("unpaired: want ErrNotPaired, got %v", err)
	}
	for _, bad := range []string{"nonsense", "host.local:80", "10.0.0.1", "0.0.0.0:80"} {
		if err := r.f.Hint(peer, bad); err == nil {
			t.Errorf("Hint(%q) should fail", bad)
		}
	}
	if len(r.f.Peers()) != 0 {
		t.Fatal("a failed hint left a device behind")
	}
	// One good address among bad ones is enough.
	if err := r.f.Hint(peer, "nonsense", "10.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.f.Get(peer); len(p.Addrs) != 1 {
		t.Fatalf("addrs = %v", p.Addrs)
	}
}

func TestForgetDropsEverything(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	r.f.Hint(peer, "100.64.0.9:4000")
	events, cancel := r.f.Subscribe()
	defer cancel()

	r.f.Forget(peer)

	expect(t, events, Disappeared)
	if len(r.f.Peers()) != 0 {
		t.Fatal("device still visible after Forget")
	}
	r.f.Forget(peer) // harmless the second time
	expectNone(t, events)
}

func TestUnpairedDeviceIsNoLongerVisible(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	if len(r.f.Peers()) != 1 {
		t.Fatal("setup failed")
	}
	r.allow.Set(peer, false) // the user unpairs it

	if len(r.f.Peers()) != 0 {
		t.Fatal("unpaired device still listed")
	}
	if _, ok := r.f.Get(peer); ok {
		t.Fatal("Get still returns an unpaired device")
	}
	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	if len(r.f.Peers()) != 0 {
		t.Fatal("unpaired device reappeared from an announcement")
	}
}

func TestSubscribers(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	a, cancelA := r.f.Subscribe()
	b, cancelB := r.f.Subscribe()

	r.f.Seen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	expect(t, a, Appeared)
	expect(t, b, Appeared)

	cancelA()
	cancelA() // safe to call twice
	if _, open := <-a; open {
		t.Fatal("channel not closed after cancel")
	}
	r.f.Gone(peer) // must not panic or block with a cancelled subscriber
	expect(t, b, Disappeared)
	cancelB()
}

func TestSlowSubscriberNeverBlocksTheFinder(t *testing.T) {
	var ids []identity.DeviceID
	for i := 0; i < eventBuffer+10; i++ {
		ids = append(ids, newID(t))
	}
	r := newRig(t, ids...)
	events, cancel := r.f.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for _, id := range ids {
			r.f.Seen(Sighting{ID: id, Addrs: []string{"192.168.1.5:4000"}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Finder blocked on a subscriber that was not reading")
	}
	if len(events) != eventBuffer {
		t.Fatalf("buffered %d events, want %d", len(events), eventBuffer)
	}
	if got := len(r.f.Peers()); got != len(ids) {
		t.Fatalf("Peers has %d entries, want %d (state must stay correct when events drop)", got, len(ids))
	}
}

func TestConcurrentUse(t *testing.T) {
	var ids []identity.DeviceID
	for i := 0; i < 6; i++ {
		ids = append(ids, newID(t))
	}
	r := newRig(t, ids...)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ev, cancel := r.f.Subscribe()
			defer cancel()
			for i := 0; i < 200; i++ {
				id := ids[(g+i)%len(ids)]
				switch i % 5 {
				case 0:
					r.f.Seen(Sighting{ID: id, Addrs: []string{"192.168.1.5:4000"}})
				case 1:
					r.f.Gone(id)
				case 2:
					r.f.Hint(id, "10.0.0.1:80")
				case 3:
					r.f.Forget(id)
				case 4:
					r.f.Peers()
					r.clock.Advance(time.Second)
				}
				select {
				case <-ev:
				default:
				}
			}
		}(g)
	}
	wg.Wait()
}

// fakeBackend is a Backend driven by the test.
type fakeBackend struct {
	advertised chan Advertisement
	ready      chan struct{} // closed once Browse is running
	failBrowse chan error
	onSeen     func(Sighting)
	onGone     func(identity.DeviceID)
	adStopped  chan struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		advertised: make(chan Advertisement, 1),
		ready:      make(chan struct{}),
		failBrowse: make(chan error, 1),
		adStopped:  make(chan struct{}),
	}
}

func (b *fakeBackend) Advertise(ctx context.Context, ad Advertisement) error {
	b.advertised <- ad
	<-ctx.Done()
	close(b.adStopped)
	return ctx.Err()
}

func (b *fakeBackend) Browse(ctx context.Context, onSeen func(Sighting), onGone func(identity.DeviceID)) error {
	b.onSeen, b.onGone = onSeen, onGone
	close(b.ready)
	select {
	case err := <-b.failBrowse:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runAsync(f *Finder, ctx context.Context, b Backend, ad *Advertisement) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- f.Run(ctx, b, ad) }()
	return ch
}

func waitRun(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestRunAdvertisesAndFeedsFinder(t *testing.T) {
	peer := newID(t)
	r := newRig(t, peer)
	b := newFakeBackend()
	ad := Advertisement{ID: r.self, Port: 4000}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r.f, ctx, b, &ad)

	select {
	case got := <-b.advertised:
		if got != ad {
			t.Fatalf("advertised %+v, want %+v", got, ad)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was advertised")
	}
	<-b.ready
	b.onSeen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	if len(r.f.Peers()) != 1 {
		t.Fatal("sighting from the backend did not reach the Finder")
	}
	b.onGone(peer)
	if len(r.f.Peers()) != 0 {
		t.Fatal("goodbye from the backend did not reach the Finder")
	}

	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("Run after cancel = %v, want nil", err)
	}
}

func TestRunBrowseOnly(t *testing.T) {
	r := newRig(t)
	b := newFakeBackend()
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r.f, ctx, b, nil)
	<-b.ready
	select {
	case <-b.advertised:
		t.Fatal("advertised although no advertisement was given")
	default:
	}
	cancel()
	if err := waitRun(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunStopsEverythingWhenBackendFails(t *testing.T) {
	r := newRig(t)
	b := newFakeBackend()
	ad := Advertisement{ID: r.self, Port: 4000}
	boom := errors.New("multicast socket failed")
	done := runAsync(r.f, context.Background(), b, &ad)
	<-b.ready
	b.failBrowse <- boom

	if err := waitRun(t, done); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want %v", err, boom)
	}
	select {
	case <-b.adStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("advertising kept running after the backend failed")
	}
}

func TestRunRejectsBadAdvertisement(t *testing.T) {
	r := newRig(t)
	b := newFakeBackend()
	err := r.f.Run(context.Background(), b, &Advertisement{ID: "bad", Port: 1})
	if !errors.Is(err, ErrBadAdvertisement) {
		t.Fatalf("want ErrBadAdvertisement, got %v", err)
	}
}

func TestRunExpiresQuietDevices(t *testing.T) {
	peer := newID(t)
	allow := func(id identity.DeviceID) bool { return id == peer }
	f := NewFinder(newID(t), allow, 150*time.Millisecond) // real clock
	events, cancelSub := f.Subscribe()
	defer cancelSub()
	b := newFakeBackend()
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(f, ctx, b, nil)
	<-b.ready

	b.onSeen(Sighting{ID: peer, Addrs: []string{"192.168.1.5:4000"}})
	expect(t, events, Appeared)
	// Nobody calls Peers or Get here, so only the background sweep can expire it.
	expect(t, events, Disappeared)

	cancel()
	waitRun(t, done)
}
