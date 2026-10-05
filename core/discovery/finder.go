package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

// DefaultTTL is how long a discovered device stays visible without being heard
// again. mDNS records typically live for two minutes.
const DefaultTTL = 2 * time.Minute

// eventBuffer is how many events a subscriber can fall behind before events
// are dropped for it.
const eventBuffer = 32

// ErrNotPaired is returned when an operation names a device that is not paired.
var ErrNotPaired = errors.New("discovery: device is not paired")

// Backend sends and receives announcements on the local network. Both methods
// block until ctx ends and return nil or ctx's error for a normal stop. Any
// other error means the backend failed.
type Backend interface {
	// Advertise announces this device until ctx ends.
	Advertise(ctx context.Context, ad Advertisement) error
	// Browse reports announcements from other devices until ctx ends. onSeen
	// may be called repeatedly for the same device. onGone is for devices that
	// say goodbye.
	Browse(ctx context.Context, onSeen func(Sighting), onGone func(identity.DeviceID)) error
}

// Visible is a paired device that can currently be reached.
type Visible struct {
	ID    identity.DeviceID
	Addrs []string // host:port, best first
	// LastSeen is when the device was last heard. It is the zero time if the
	// device is only known from a manual hint.
	LastSeen time.Time
	// Hinted is true if the user supplied an address for this device.
	Hinted bool
}

// EventKind says what changed.
type EventKind int

const (
	// Appeared means a device became visible.
	Appeared EventKind = iota + 1
	// Updated means a visible device's addresses changed.
	Updated
	// Disappeared means a device is no longer visible.
	Disappeared
)

func (k EventKind) String() string {
	switch k {
	case Appeared:
		return "appeared"
	case Updated:
		return "updated"
	case Disappeared:
		return "disappeared"
	}
	return fmt.Sprintf("EventKind(%d)", int(k))
}

// Event reports a change to Finder's view. For Disappeared, Peer holds the
// last known state.
type Event struct {
	Kind EventKind
	Peer Visible
}

type entry struct {
	seen     []string  // from announcements
	lastSeen time.Time // when seen was last refreshed
	hinted   []string  // from the user
}

// Finder tracks which paired devices are visible on the local network. It is
// safe for concurrent use.
//
// Only devices for which allow returns true are tracked, and this device's own
// id is ignored, so announcements from strangers never use any memory.
type Finder struct {
	self  identity.DeviceID
	allow func(identity.DeviceID) bool
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[identity.DeviceID]*entry
	subs    map[int]chan Event
	nextSub int
}

// NewFinder creates a Finder. allow decides which ids are tracked (normally
// pairing.Store.Allow). ttl is how long a heard device stays visible; zero
// means DefaultTTL.
func NewFinder(self identity.DeviceID, allow func(identity.DeviceID) bool, ttl time.Duration) *Finder {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Finder{
		self:    self,
		allow:   allow,
		ttl:     ttl,
		now:     time.Now,
		entries: map[identity.DeviceID]*entry{},
		subs:    map[int]chan Event{},
	}
}

// Seen records an announcement. Backends call it, and so can native code such
// as the iOS Bonjour browser. Announcements from this device, from unpaired
// devices, or with no acceptable address are ignored.
func (f *Finder) Seen(s Sighting) {
	if s.ID == f.self || !f.allow(s.ID) {
		return
	}
	addrs := cleanAddrs(s.Addrs, true)
	if len(addrs) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateLocked(s.ID, func(e *entry) {
		e.seen = addrs
		e.lastSeen = f.now()
	})
}

// Gone records that a device said goodbye. A manual hint, if any, remains.
func (f *Finder) Gone(id identity.DeviceID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateLocked(id, func(e *entry) {
		e.seen = nil
		e.lastSeen = time.Time{}
	})
}

// Hint sets addresses for a paired device by hand, for networks where
// announcements do not get through. Hints never expire. Passing no addresses
// removes the hint. Unlike announcements, hinted addresses may be anywhere (a
// VPN address, for example), but must be an IP address with a port.
func (f *Finder) Hint(id identity.DeviceID, addrs ...string) error {
	if !f.allow(id) {
		return ErrNotPaired
	}
	clean := cleanAddrs(addrs, false)
	if len(addrs) > 0 && len(clean) == 0 {
		return fmt.Errorf("discovery: none of the given addresses are usable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateLocked(id, func(e *entry) { e.hinted = clean })
	return nil
}

// Forget drops everything known about a device, hints included. Call it when
// a device is unpaired.
func (f *Finder) Forget(id identity.DeviceID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateLocked(id, func(e *entry) {
		e.seen, e.lastSeen, e.hinted = nil, time.Time{}, nil
	})
}

// Get returns a visible device.
func (f *Finder) Get(id identity.DeviceID) (Visible, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireLocked()
	return f.visibleLocked(id)
}

// Peers returns all visible paired devices, ordered by id.
func (f *Finder) Peers() []Visible {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireLocked()
	out := make([]Visible, 0, len(f.entries))
	for id := range f.entries {
		if v, ok := f.visibleLocked(id); ok {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Subscribe returns a channel of changes and a function that stops the
// subscription and closes the channel. If a subscriber falls more than 32
// events behind, further events are dropped for it, so after a gap call Peers
// to get the current state.
func (f *Finder) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, eventBuffer)
	f.mu.Lock()
	n := f.nextSub
	f.nextSub++
	f.subs[n] = ch
	f.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.subs, n)
			f.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Run drives a Backend: it advertises ad (unless ad is nil, for a browse-only
// device), feeds what the backend hears into the Finder, and expires devices
// that go quiet. It returns when ctx ends or the backend fails.
func (f *Finder) Run(ctx context.Context, b Backend, ad *Advertisement) error {
	if ad != nil {
		if err := ad.Validate(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	const maxTasks = 2
	errc := make(chan error, maxTasks)
	tasks := 0
	if ad != nil {
		tasks++
		go func() { errc <- b.Advertise(ctx, *ad) }()
	}
	tasks++
	go func() { errc <- b.Browse(ctx, f.Seen, f.Gone) }()
	go f.sweep(ctx)

	first := <-errc
	cancel()
	for i := 1; i < tasks; i++ {
		<-errc
	}
	if errors.Is(first, context.Canceled) {
		return nil
	}
	return first
}

// sweep expires quiet devices until ctx ends.
func (f *Finder) sweep(ctx context.Context) {
	interval := f.ttl / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.mu.Lock()
			f.expireLocked()
			f.mu.Unlock()
		}
	}
}

// expireLocked drops announcements older than the TTL.
func (f *Finder) expireLocked() {
	cutoff := f.now().Add(-f.ttl)
	for id, e := range f.entries {
		if len(e.seen) > 0 && e.lastSeen.Before(cutoff) {
			f.updateLocked(id, func(e *entry) {
				e.seen = nil
				e.lastSeen = time.Time{}
			})
		}
	}
}

// updateLocked applies change to the entry for id, then emits whichever event
// describes the difference. Entries with nothing left are deleted.
func (f *Finder) updateLocked(id identity.DeviceID, change func(*entry)) {
	before, hadBefore := f.visibleLocked(id)

	e, ok := f.entries[id]
	if !ok {
		e = &entry{}
		f.entries[id] = e
	}
	change(e)
	if len(e.seen) == 0 && len(e.hinted) == 0 {
		delete(f.entries, id)
	}

	after, hasAfter := f.visibleLocked(id)
	switch {
	case !hadBefore && hasAfter:
		f.emitLocked(Event{Appeared, after})
	case hadBefore && !hasAfter:
		f.emitLocked(Event{Disappeared, before})
	case hadBefore && hasAfter && !equalStrings(before.Addrs, after.Addrs):
		f.emitLocked(Event{Updated, after})
	}
}

// visibleLocked builds the public view of an entry. A device that has since
// been unpaired is not visible, even if an entry is still held.
func (f *Finder) visibleLocked(id identity.DeviceID) (Visible, bool) {
	e, ok := f.entries[id]
	if !ok || !f.allow(id) {
		return Visible{}, false
	}
	addrs := tidy(append(append([]string(nil), e.seen...), e.hinted...))
	if len(addrs) == 0 {
		return Visible{}, false
	}
	return Visible{ID: id, Addrs: addrs, LastSeen: e.lastSeen, Hinted: len(e.hinted) > 0}, true
}

func (f *Finder) emitLocked(ev Event) {
	for _, ch := range f.subs {
		select {
		case ch <- ev:
		default: // subscriber is too slow, it can resync with Peers
		}
	}
}

func equalStrings(a, b []string) bool {
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
