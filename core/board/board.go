// Package board is the combined view of every file the user can see: this
// device's own files plus the files other paired devices are sharing.
//
// It does the work behind a file list screen. It asks each visible paired
// device for its list when the device appears and then every RefreshEvery,
// keeps the last list it got from each device, and tells subscribers when
// something changed. A file is pulled from the device that holds it, so a file
// whose device is not currently reachable is shown with Reachable false.
//
// Lists are not passed on from one device to another. Each file is only ever
// offered by the device that holds it, which keeps the rules simple: a device
// cannot make up files on behalf of another one.
package board

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
)

// DefaultRefresh is how often visible devices are asked for their lists again.
const DefaultRefresh = 30 * time.Second

// ErrUnreachable means the device holding the file is not visible right now.
var ErrUnreachable = errors.New("board: the device holding this file cannot be reached")

// ErrUnknownFile means the board has no such file.
var ErrUnknownFile = errors.New("board: unknown file")

// Remote fetches from other devices. transfer.Client implements it.
type Remote interface {
	List(ctx context.Context, addr string, peer identity.DeviceID) ([]shelf.Entry, error)
	Pull(ctx context.Context, addr string, peer identity.DeviceID, id, dir string) (string, shelf.Entry, error)
}

// Item is one file on the board.
type Item struct {
	shelf.Entry
	// Holder is the device that has the file. It is this device's own id for
	// local files.
	Holder identity.DeviceID
	// Local is true for this device's own files.
	Local bool
	// Reachable is true if the file can be pulled right now. Local files are
	// always reachable.
	Reachable bool
}

type peerState struct {
	entries []shelf.Entry
	fetched time.Time
	err     error
}

// Board is safe for concurrent use.
type Board struct {
	self   identity.DeviceID
	local  *shelf.Shelf // may be nil
	finder *discovery.Finder
	remote Remote
	allow  func(identity.DeviceID) bool
	now    func() time.Time

	// RefreshEvery is how often visible devices are asked again. Zero means
	// DefaultRefresh. Set it before Run.
	RefreshEvery time.Duration

	mu       sync.Mutex
	peers    map[identity.DeviceID]*peerState
	inflight map[identity.DeviceID]bool
	subs     map[int]chan struct{}
	nextSub  int
}

// New creates a Board. local may be nil for a device that shares nothing. allow
// says which devices are paired (normally pairing.Store.Allow): files of other
// devices are never shown or pulled.
func New(self identity.DeviceID, local *shelf.Shelf, finder *discovery.Finder, remote Remote, allow func(identity.DeviceID) bool) *Board {
	return &Board{
		self: self, local: local, finder: finder, remote: remote, allow: allow, now: time.Now,
		peers:    map[identity.DeviceID]*peerState{},
		inflight: map[identity.DeviceID]bool{},
		subs:     map[int]chan struct{}{},
	}
}

func (b *Board) refreshEvery() time.Duration {
	if b.RefreshEvery > 0 {
		return b.RefreshEvery
	}
	return DefaultRefresh
}

// Run keeps the board up to date until ctx ends.
func (b *Board) Run(ctx context.Context) {
	events, cancel := b.finder.Subscribe()
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()

	refreshAll := func() {
		for _, v := range b.finder.Peers() {
			wg.Add(1)
			go func() { defer wg.Done(); b.Refresh(ctx, v.ID) }()
		}
	}
	refreshAll()
	tick := time.NewTicker(b.refreshEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Kind {
			case discovery.Appeared, discovery.Updated:
				wg.Add(1)
				go func() { defer wg.Done(); b.Refresh(ctx, ev.Peer.ID) }()
			case discovery.Disappeared:
				b.notify() // files of that device are now unreachable
			}
		case <-tick.C:
			refreshAll()
			b.notify() // expired files drop out of the list
		}
	}
}

// Refresh asks one device for its list now. It does nothing if a refresh of
// that device is already running. A failed refresh keeps the old list.
func (b *Board) Refresh(ctx context.Context, id identity.DeviceID) error {
	b.mu.Lock()
	if b.inflight[id] {
		b.mu.Unlock()
		return nil
	}
	b.inflight[id] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.inflight, id)
		b.mu.Unlock()
	}()

	v, ok := b.finder.Get(id)
	if !ok {
		return ErrUnreachable
	}
	var entries []shelf.Entry
	var err error
	for _, addr := range v.Addrs {
		entries, err = b.remote.List(ctx, addr, id)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	b.mu.Lock()
	if err == nil && !b.allow(id) {
		// unpaired while we were asking
		b.mu.Unlock()
		return ErrUnreachable
	}
	st := b.peers[id]
	if st == nil {
		st = &peerState{}
		b.peers[id] = st
	}
	changed := false
	if err == nil {
		changed = !sameEntries(st.entries, entries)
		st.entries, st.fetched = entries, b.now()
	}
	st.err = err
	b.mu.Unlock()
	if changed {
		b.notify()
	}
	return err
}

func sameEntries(a, b []shelf.Entry) bool {
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

// Forget drops what the board knows about a device. Call it on unpairing.
func (b *Board) Forget(id identity.DeviceID) {
	b.mu.Lock()
	_, had := b.peers[id]
	delete(b.peers, id)
	b.mu.Unlock()
	if had {
		b.notify()
	}
}

// Items returns every file that has not expired, newest first. A file is
// listed once per device that holds it.
func (b *Board) Items() []Item {
	now := b.now()
	var out []Item
	if b.local != nil {
		for _, e := range b.local.List() {
			out = append(out, Item{Entry: e, Holder: b.self, Local: true, Reachable: true})
		}
	}
	reachable := map[identity.DeviceID]bool{}
	for _, v := range b.finder.Peers() {
		reachable[v.ID] = true
	}
	b.mu.Lock()
	for id, st := range b.peers {
		if !b.allow(id) {
			continue
		}
		for _, e := range st.entries {
			if e.Expired(now) {
				continue
			}
			out = append(out, Item{Entry: e, Holder: id, Reachable: reachable[id]})
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if !a.Created.Equal(c.Created) {
			return a.Created.After(c.Created)
		}
		if a.ID != c.ID {
			return a.ID < c.ID
		}
		return a.Holder < c.Holder
	})
	return out
}

// Pull downloads a file held by another device into dir. holder says which
// device to ask. It returns the path of the saved file.
func (b *Board) Pull(ctx context.Context, holder identity.DeviceID, fileID, dir string) (string, shelf.Entry, error) {
	if !b.allow(holder) || !b.has(holder, fileID) {
		return "", shelf.Entry{}, ErrUnknownFile
	}
	v, ok := b.finder.Get(holder)
	if !ok {
		return "", shelf.Entry{}, ErrUnreachable
	}
	var lastErr error
	for _, addr := range v.Addrs {
		path, e, err := b.remote.Pull(ctx, addr, holder, fileID, dir)
		if err == nil {
			return path, e, nil
		}
		lastErr = err
		if ctx.Err() != nil || !retryable(err) {
			break
		}
	}
	if lastErr == nil {
		lastErr = ErrUnreachable
	}
	return "", shelf.Entry{}, fmt.Errorf("board: pull %s: %w", fileID, lastErr)
}

func (b *Board) has(holder identity.DeviceID, fileID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.peers[holder]
	if st == nil {
		return false
	}
	for _, e := range st.entries {
		if e.ID == fileID && !e.Expired(b.now()) {
			return true
		}
	}
	return false
}

// retryable reports whether another address of the same device is worth
// trying. Errors about the file itself are not.
func retryable(err error) bool {
	return !errors.Is(err, shelf.ErrNotFound)
}

// LastError returns the error from the most recent attempt to fetch a device's
// list, or nil.
func (b *Board) LastError(id identity.DeviceID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.peers[id]; st != nil {
		return st.err
	}
	return nil
}

// Subscribe returns a channel that receives a value whenever the result of
// Items may have changed. Notifications are merged, so a slow reader sees at
// most one waiting value. The returned function stops the subscription.
func (b *Board) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	n := b.nextSub
	b.nextSub++
	b.subs[n] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, n)
			b.mu.Unlock()
		})
	}
}

func (b *Board) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
