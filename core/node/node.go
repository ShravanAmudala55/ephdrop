// Package node puts the core together into one object that an app can use: it
// owns this device's identity, paired devices, shared files, discovery and the
// transfer server, and offers a small set of plain methods on top of them.
//
// Desktop apps talk to it through the local HTTP API (package api). Mobile apps
// will use it through gomobile.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/board"
	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/discovery/mdns"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/pairing"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
	"github.com/ShravanAmudala55/ephdrop/core/transfer"
)

const (
	defaultSweepEvery  = time.Minute
	pokeTimeout        = 5 * time.Second
	inviteLifetime     = 5 * time.Minute
	confirmWaitDefault = 2 * time.Minute
)

var (
	// ErrNotRunning means Start has not been called, or the node has stopped.
	ErrNotRunning = errors.New("node: not running")
	// ErrNotLocal means the file belongs to another device.
	ErrNotLocal = errors.New("node: that file is on another device")
	// ErrNoNetwork means there is no network address to pair over.
	ErrNoNetwork = errors.New("node: no network address found. Are you connected to Wi-Fi?")
)

// Config says where the node keeps its data and how it behaves.
type Config struct {
	// Dir holds the identity, paired devices and shared files.
	Dir string
	// Name is how this device is shown to others. Defaults to the host name.
	Name string
	// DownloadDir is where Fetch saves files by default. Defaults to Dir/downloads.
	DownloadDir string
	// Listen is the address for the transfer server. Defaults to ":0".
	Listen string
	// Backend finds other devices. Defaults to multicast DNS.
	Backend discovery.Backend
	// LocalIPs lists this device's own IPv4 addresses for invites. Phone apps
	// supply it because a Go program on Android may not list interfaces. If
	// nil, the interfaces are listed.
	LocalIPs func() []string

	// Intervals. Zero means the default.
	RefreshEvery time.Duration
	SweepEvery   time.Duration
}

// Node is safe for concurrent use.
type Node struct {
	cfg    Config
	id     *identity.Identity
	store  *pairing.Store
	shelf  *shelf.Shelf
	finder *discovery.Finder
	board  *board.Board
	client *transfer.Client

	mu      sync.Mutex
	port    int
	running bool
	runCtx  context.Context // set while running
	done    chan struct{}
	subs    map[int]chan struct{}
	nextSub int
}

// New opens (or creates) the node's data. It does not touch the network until
// Start is called.
func New(cfg Config) (*Node, error) {
	if cfg.Dir == "" {
		return nil, errors.New("node: no data directory")
	}
	if cfg.Name == "" {
		cfg.Name = hostName()
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = filepath.Join(cfg.Dir, "downloads")
	}
	if cfg.Listen == "" {
		cfg.Listen = ":0"
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = defaultSweepEvery
	}
	id, err := identity.LoadOrCreate(cfg.Dir)
	if err != nil {
		return nil, err
	}
	store, err := pairing.OpenStore(filepath.Join(cfg.Dir, "peers.json"))
	if err != nil {
		return nil, err
	}
	sh, err := shelf.Open(filepath.Join(cfg.Dir, "shelf"), id.ID())
	if err != nil {
		return nil, err
	}
	cert, err := id.TLSCertificate()
	if err != nil {
		return nil, err
	}
	n := &Node{cfg: cfg, id: id, store: store, shelf: sh, subs: map[int]chan struct{}{}}
	n.finder = discovery.NewFinder(id.ID(), store.Allow, 0)
	n.client = &transfer.Client{Cert: cert}
	n.board = board.New(id.ID(), sh, n.finder, n.client, store.Allow)
	n.board.RefreshEvery = cfg.RefreshEvery
	return n, nil
}

func hostName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "this device"
}

// ID returns this device's id.
func (n *Node) ID() identity.DeviceID { return n.id.ID() }

// Name returns the name shown to other devices.
func (n *Node) Name() string { return n.cfg.Name }

// DownloadDir returns the default folder for downloaded files.
func (n *Node) DownloadDir() string { return n.cfg.DownloadDir }

// Port returns the port the transfer server listens on, or 0 before Start.
func (n *Node) Port() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.port
}

// Start begins serving files, announcing this device and looking for others.
// It returns once everything is running. Stop it by cancelling ctx and then
// call Wait.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return errors.New("node: already running")
	}
	ln, err := net.Listen("tcp", n.cfg.Listen)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	n.port = ln.Addr().(*net.TCPAddr).Port
	n.running = true
	n.done = make(chan struct{})
	n.mu.Unlock()

	cert, err := n.id.TLSCertificate()
	if err != nil {
		ln.Close()
		n.markStopped()
		return err
	}
	backend := n.cfg.Backend
	if backend == nil {
		backend = &mdns.Backend{}
	}
	ctx, cancel := context.WithCancel(ctx)
	n.mu.Lock()
	n.runCtx = ctx
	n.mu.Unlock()
	srv := &transfer.Server{
		Cert: cert, Allow: n.store.Allow, Shelf: n.shelf,
		// A device saying its files changed: ask it for its list right away.
		OnPoke: func(from identity.DeviceID) {
			go n.board.Refresh(ctx, from)
		},
	}

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	run(func() { srv.Serve(ctx, ln) })
	run(func() {
		// If discovery cannot start (no network yet), the node still serves.
		n.finder.Run(ctx, backend, &discovery.Advertisement{ID: n.id.ID(), Port: n.port})
	})
	run(func() { n.board.Run(ctx) })
	run(func() { n.sweepLoop(ctx) })
	run(func() { n.forwardBoardChanges(ctx) })
	run(func() { n.forwardPeerChanges(ctx) })
	go func() {
		wg.Wait()
		cancel()
		n.markStopped()
	}()
	return nil
}

func (n *Node) markStopped() {
	n.mu.Lock()
	n.running = false
	n.runCtx = nil
	d := n.done
	n.mu.Unlock()
	if d != nil {
		close(d)
	}
}

// Wait blocks until a started node has stopped.
func (n *Node) Wait() {
	n.mu.Lock()
	d := n.done
	n.mu.Unlock()
	if d != nil {
		<-d
	}
}

func (n *Node) sweepLoop(ctx context.Context) {
	if c, err := n.shelf.Sweep(); err == nil && c > 0 {
		n.notify()
	}
	t := time.NewTicker(n.cfg.SweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c, err := n.shelf.Sweep(); err == nil && c > 0 {
				n.notify()
			}
		}
	}
}

func (n *Node) forwardBoardChanges(ctx context.Context) {
	ch, cancel := n.board.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			n.notify()
		}
	}
}

func (n *Node) forwardPeerChanges(ctx context.Context) {
	ch, cancel := n.finder.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			n.notify() // someone came online or went offline
		}
	}
}

// ---------------------------------------------------------------- files

// Items returns every file visible to this device, newest first.
func (n *Node) Items() []board.Item { return n.board.Items() }

// Share copies r into the shelf and shares it with paired devices. ttl is how
// long it lives (zero means 24 hours, at most 7 days).
func (n *Node) Share(r io.Reader, name string, ttl time.Duration) (shelf.Entry, error) {
	e, err := n.shelf.Add(r, name, ttl, 0)
	if err == nil {
		n.notify()
		n.pokePeers()
	}
	return e, err
}

// ShareFile shares the file at path.
func (n *Node) ShareFile(path string, ttl time.Duration) (shelf.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return shelf.Entry{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return shelf.Entry{}, err
	}
	if !fi.Mode().IsRegular() {
		return shelf.Entry{}, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return n.Share(f, filepath.Base(path), ttl)
}

// Remove stops sharing one of this device's own files and deletes it.
func (n *Node) Remove(id string) error {
	err := n.shelf.Remove(id)
	if err == nil {
		n.notify()
		n.pokePeers()
	}
	return err
}

// Local opens one of this device's own files for reading. The caller closes it.
func (n *Node) Local(id string) (*os.File, shelf.Entry, error) { return n.shelf.Open(id) }

// Fetch downloads a file held by another device into dir (the download folder
// if dir is empty) and returns where it was saved.
func (n *Node) Fetch(ctx context.Context, holder identity.DeviceID, fileID, dir string) (string, shelf.Entry, error) {
	if !n.isRunning() {
		return "", shelf.Entry{}, ErrNotRunning
	}
	if holder == n.id.ID() {
		return "", shelf.Entry{}, errors.New("node: that file is already on this device")
	}
	if dir == "" {
		dir = n.cfg.DownloadDir
	}
	return n.board.Pull(ctx, holder, fileID, dir)
}

func (n *Node) isRunning() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.running
}

// ---------------------------------------------------------------- devices

// PeerInfo is a paired device and whether it can be reached right now.
type PeerInfo struct {
	pairing.Peer
	Online bool
}

// Peers returns the paired devices, with the visible ones marked online.
func (n *Node) Peers() []PeerInfo {
	online := map[identity.DeviceID]bool{}
	for _, v := range n.finder.Peers() {
		online[v.ID] = true
	}
	var out []PeerInfo
	for _, p := range n.store.List() {
		out = append(out, PeerInfo{Peer: p, Online: online[p.ID]})
	}
	return out
}

// Unpair forgets a paired device. Its files disappear from the list and it can
// no longer connect.
func (n *Node) Unpair(id identity.DeviceID) error {
	if err := n.store.Remove(id); err != nil {
		return err
	}
	n.finder.Forget(id)
	n.board.Forget(id)
	n.notify()
	return nil
}

// Hint tells the node an address for a paired device, for when automatic
// finding does not work (another subnet, a VPN).
func (n *Node) Hint(id identity.DeviceID, addrs ...string) error {
	return n.finder.Hint(id, addrs...)
}

// ---------------------------------------------------------------- pairing

// PairRequest is a device that wants to pair with an invite this device made.
// Call Reply(true) to accept or Reply(false) to decline.
type PairRequest struct {
	Peer  pairing.Peer
	Reply func(accept bool)
}

// PairResult says how an invite ended.
type PairResult struct {
	Peer pairing.Peer
	Err  error
}

// InviteSession is an invite waiting for another device to join.
type InviteSession struct {
	// Text is the invite to show as a QR code or give to the other device.
	Text string
	// Expires is when the invite stops working.
	Expires time.Time
	// Requests delivers a device that wants to pair, once it has proved it holds
	// the invite. The answer must be given with Reply.
	Requests <-chan PairRequest
	// Done delivers the outcome once, then is closed.
	Done   <-chan PairResult
	cancel context.CancelFunc
}

// Cancel ends the invite.
func (s *InviteSession) Cancel() { s.cancel() }

// StartInvite makes an invite and waits in the background for another device
// to join with it.
func (n *Node) StartInvite(ctx context.Context) (*InviteSession, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	var addrs []string
	if n.cfg.LocalIPs != nil {
		addrs = pairing.AddrsFromIPs(n.cfg.LocalIPs(), port)
	} else {
		addrs = pairing.LocalAddrs(port)
	}
	if len(addrs) == 0 {
		ln.Close()
		return nil, ErrNoNetwork
	}
	inv, err := pairing.NewInvite(n.id, addrs, inviteLifetime)
	if err != nil {
		ln.Close()
		return nil, err
	}
	ictx, cancel := context.WithCancel(ctx)
	reqs := make(chan PairRequest)
	done := make(chan PairResult, 1)
	sess := &InviteSession{Text: inv.String(), Expires: inv.Expires, Requests: reqs, Done: done, cancel: cancel}

	v := &pairing.Inviter{
		Self: n.id, Name: n.cfg.Name, Store: n.store, Invite: inv,
		Timeout: confirmWaitDefault,
		Confirm: func(p pairing.Peer) bool {
			answer := make(chan bool, 1)
			var once sync.Once
			req := PairRequest{Peer: p, Reply: func(ok bool) { once.Do(func() { answer <- ok }) }}
			select {
			case reqs <- req:
			case <-ictx.Done():
				return false
			}
			select {
			case ok := <-answer:
				return ok
			case <-ictx.Done():
				return false
			}
		},
	}
	go func() {
		defer cancel()
		peer, err := v.Serve(ictx, ln)
		if err == nil {
			n.finder.Forget(peer.ID) // start from a clean slate for the new device
			n.notify()
		}
		done <- PairResult{Peer: peer, Err: err}
		close(done)
	}()
	return sess, nil
}

// Join pairs with the device that made the invite text.
func (n *Node) Join(ctx context.Context, invite string) (pairing.Peer, error) {
	inv, err := pairing.ParseInvite(invite)
	if err != nil {
		return pairing.Peer{}, err
	}
	peer, err := pairing.Join(ctx, n.id, n.cfg.Name, n.store, inv)
	if err == nil {
		n.notify()
	}
	return peer, err
}

// ---------------------------------------------------------------- changes

// Subscribe returns a channel that receives a value whenever something an app
// shows may have changed: files, paired devices, or who is online. Values are
// merged, so a slow reader sees at most one waiting. The function stops the
// subscription.
func (n *Node) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	k := n.nextSub
	n.nextSub++
	n.subs[k] = ch
	n.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			n.mu.Lock()
			delete(n.subs, k)
			n.mu.Unlock()
		})
	}
}

func (n *Node) notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// pokePeers tells every visible paired device that this device's files
// changed, so they show the change at once instead of at their next refresh.
func (n *Node) pokePeers() {
	n.mu.Lock()
	ctx := n.runCtx
	n.mu.Unlock()
	if ctx == nil {
		return
	}
	for _, v := range n.finder.Peers() {
		go func() {
			pctx, cancel := context.WithTimeout(ctx, pokeTimeout)
			defer cancel()
			for _, addr := range v.Addrs {
				if n.client.Poke(pctx, addr, v.ID) == nil {
					return
				}
			}
		}()
	}
}
