// Package mobile is what the phone apps embed. It is the whole ephdrop program
// behind a handful of simple calls, written so gomobile can turn it into an
// Android library (.aar) and an iOS framework.
//
// The phone app shows the same window as the desktop app: Start runs the node
// and its local web server, and the app opens URL in a web view. The app does
// the few things only it can do:
//
//   - Find this device's Wi-Fi address (Native.LocalIPs).
//   - Advertise this device and look for others with the system's own
//     Bonjour or NSD service, then report what it hears with Seen and Gone.
//     A Go program on a phone is often not allowed to do this itself.
//   - Keep the program running in the background (a foreground service).
package mobile

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ShravanAmudala55/ephdrop/core/api"
	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/node"
)

// Native is implemented by the phone app.
type Native interface {
	// LocalIPs returns this device's IPv4 addresses on the local network,
	// separated by commas.
	LocalIPs() string
}

// Agent is a running ephdrop node.
type Agent struct {
	node   *node.Node
	srv    *api.Server
	back   *nativeBackend
	cancel context.CancelFunc
	port   int
	done   chan struct{}
	once   sync.Once
}

// Start runs a node that keeps its data in dir. name is how this device is
// shown to others and downloads is where saved files go. It returns once the
// node is running.
func Start(dir, name, downloads string, native Native) (*Agent, error) {
	// On Android the default temporary folder is not writable. Use one inside
	// the app's own data instead.
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	os.Setenv("TMPDIR", tmp)
	back := &nativeBackend{}
	cfg := node.Config{Dir: dir, Name: name, DownloadDir: downloads, Listen: ":0", Backend: back}
	if native != nil {
		cfg.LocalIPs = func() []string { return splitList(native.LocalIPs()) }
	}
	n, err := node.New(cfg)
	if err != nil {
		return nil, err
	}
	srv, err := api.New(n)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := n.Start(ctx); err != nil {
		cancel()
		ln.Close()
		return nil, err
	}
	a := &Agent{node: n, srv: srv, back: back, cancel: cancel, port: ln.Addr().(*net.TCPAddr).Port, done: make(chan struct{})}
	go func() {
		srv.Serve(ctx, ln)
		cancel()
		n.Wait()
		close(a.done)
	}()
	return a, nil
}

// PageURL is the address of the window's page. Opening it signs the web view in.
func (a *Agent) PageURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", a.port, a.srv.Token())
}

// Token is the secret the app can send as "Authorization: Bearer ..." when it
// talks to the local server itself, for example to share a file from the
// system share sheet.
func (a *Agent) Token() string { return a.srv.Token() }

// Port is the local web server's port, on 127.0.0.1.
func (a *Agent) Port() int { return a.port }

// DeviceID is this device's id. Put it in the advertisement.
func (a *Agent) DeviceID() string { return string(a.node.ID()) }

// TransferPort is the port other devices connect to. Put it in the
// advertisement.
func (a *Agent) TransferPort() int { return a.node.Port() }

// Seen reports a device heard on the network. ips are comma separated, and
// version is the "v" value of its advertisement. Anything that is not a
// valid ephdrop advertisement is refused.
func (a *Agent) Seen(id, ips string, port int, version string) error {
	var addrs []netip.Addr
	for _, s := range splitList(ips) {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		addrs = append(addrs, ip)
	}
	s, err := discovery.SightingFromService([]string{"v=" + version, "id=" + id}, port, addrs)
	if err != nil {
		return err
	}
	if s.ID == a.node.ID() {
		return nil // our own advertisement
	}
	a.back.seen(s)
	return nil
}

// Gone reports that a device said goodbye.
func (a *Agent) Gone(id string) { a.back.gone(identity.DeviceID(id)) }

// Stop stops the node and waits for it to finish. It is safe to call twice.
func (a *Agent) Stop() {
	a.once.Do(a.cancel)
	<-a.done
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// nativeBackend lets the phone app do the finding. Advertising is done by the
// app, so Advertise only waits. Browse hands over the app's reports. Reports
// that arrive before Browse has started are kept and handed over then.
type nativeBackend struct {
	mu      sync.Mutex
	onSeen  func(discovery.Sighting)
	onGone  func(identity.DeviceID)
	pending map[identity.DeviceID]discovery.Sighting
}

func (b *nativeBackend) Advertise(ctx context.Context, ad discovery.Advertisement) error {
	if err := ad.Validate(); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (b *nativeBackend) Browse(ctx context.Context, onSeen func(discovery.Sighting), onGone func(identity.DeviceID)) error {
	b.mu.Lock()
	b.onSeen, b.onGone = onSeen, onGone
	early := b.pending
	b.pending = nil
	b.mu.Unlock()
	for _, s := range early {
		onSeen(s)
	}
	<-ctx.Done()
	b.mu.Lock()
	b.onSeen, b.onGone = nil, nil
	b.mu.Unlock()
	return nil
}

func (b *nativeBackend) seen(s discovery.Sighting) {
	b.mu.Lock()
	f := b.onSeen
	if f == nil {
		if b.pending == nil {
			b.pending = map[identity.DeviceID]discovery.Sighting{}
		}
		b.pending[s.ID] = s
	}
	b.mu.Unlock()
	if f != nil {
		f(s)
	}
}

func (b *nativeBackend) gone(id identity.DeviceID) {
	b.mu.Lock()
	f := b.onGone
	delete(b.pending, id)
	b.mu.Unlock()
	if f != nil {
		f(id)
	}
}
