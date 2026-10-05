// Package transfer moves files between paired devices.
//
// A connection is TLS 1.3 with both sides pinned to paired device keys (see
// identity.PinnedConfig), so only paired devices can talk to each other. One
// connection carries one request.
//
// The client sends a single JSON line, either {"op":"list"} or
// {"op":"get","id":"..."}. The server answers with a single JSON line. For a
// get that succeeded, the file's bytes follow the line, exactly Entry.Size of
// them. The client checks the size and the SHA-256 hash before it keeps the
// file, so a faulty or hostile peer cannot hand over a corrupted file.
package transfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
)

const (
	maxRequestBytes  = 1024
	maxResponseBytes = 4 << 20
	maxListEntries   = 10000
	handshakeTimeout = 10 * time.Second
	// idleTimeout is how long a connection may go without moving any data.
	idleTimeout = 30 * time.Second
	// maxConns is how many connections a server handles at once.
	maxConns = 16
)

var (
	// ErrWrongPeer means the device at the address is not the one we asked for.
	ErrWrongPeer = errors.New("transfer: connected to a different device than expected")
	// ErrBadFile means the file we received does not match what the peer said.
	ErrBadFile = errors.New("transfer: received file does not match its description")
	// ErrTooLarge means the file is bigger than the client allows.
	ErrTooLarge = errors.New("transfer: file too large")
	// ErrBadResponse means the peer answered with something we cannot use.
	ErrBadResponse = errors.New("transfer: bad response from peer")
)

// RemoteError is an error the peer reported.
type RemoteError struct{ Message string }

func (e *RemoteError) Error() string { return "transfer: peer said: " + e.Message }

type request struct {
	Op string `json:"op"`
	ID string `json:"id,omitempty"`
}

type response struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code,omitempty"` // "not_found", "bad_request", "internal"
	Error   string        `json:"error,omitempty"`
	Entries []shelf.Entry `json:"entries,omitempty"`
	Entry   *shelf.Entry  `json:"entry,omitempty"`
}

// deadlineConn pushes the deadline forward on every read and write, so a
// connection is closed only when it stops moving data.
type deadlineConn struct {
	net.Conn
	idle time.Duration
	stop func() bool // ends the context watcher, if there is one
}

func (c deadlineConn) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return c.Conn.Close()
}

func (c deadlineConn) Read(p []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c deadlineConn) Write(p []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// readLine reads up to max bytes ending in a newline.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && buf.Len() > 0 {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b == '\n' {
			return buf.Bytes(), nil
		}
		if buf.Len() >= max {
			return nil, fmt.Errorf("%w: line longer than %d bytes", ErrBadResponse, max)
		}
		buf.WriteByte(b)
	}
}

// ---------------------------------------------------------------- server

// Server answers list and get requests from paired devices.
type Server struct {
	Cert  tls.Certificate
	Allow func(identity.DeviceID) bool // true for paired devices
	Shelf *shelf.Shelf
	// Logf, if set, receives one line about each failed connection.
	Logf func(format string, args ...any)

	// test hooks
	idle      time.Duration
	handshake time.Duration
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Server) handshakeTime() time.Duration {
	if s.handshake > 0 {
		return s.handshake
	}
	return handshakeTimeout
}

func (s *Server) idleTime() time.Duration {
	if s.idle > 0 {
		return s.idle
	}
	return idleTimeout
}

// Serve accepts connections on ln until ctx ends, then closes ln and waits for
// running connections to finish. It returns nil on a normal stop.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	cfg := identity.PinnedConfig(s.Cert, s.Allow)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, maxConns)
	for {
		// Wait for a free slot before accepting, so extra clients queue up in
		// the operating system instead of being turned away.
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		conn, err := ln.Accept()
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer conn.Close()
			// A cancelled context closes running connections too.
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			if err := s.handle(ctx, conn, cfg); err != nil && ctx.Err() == nil {
				s.logf("transfer: %v: %v", conn.RemoteAddr(), err)
			}
		}()
	}
}

func (s *Server) handle(ctx context.Context, raw net.Conn, cfg *tls.Config) error {
	tc := tls.Server(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, s.handshakeTime())
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	conn := deadlineConn{Conn: tc, idle: s.idleTime()}
	r := bufio.NewReaderSize(conn, 4096)
	line, err := readLine(r, maxRequestBytes)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return writeResponse(conn, response{Code: "bad_request", Error: "unreadable request"})
	}
	switch req.Op {
	case "list":
		return s.list(conn)
	case "get":
		return s.get(conn, req.ID)
	default:
		return writeResponse(conn, response{Code: "bad_request", Error: "unknown operation"})
	}
}

func writeResponse(w io.Writer, r response) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func (s *Server) list(w io.Writer) error {
	entries := s.Shelf.List()
	if len(entries) > maxListEntries {
		entries = entries[:maxListEntries]
	}
	return writeResponse(w, response{OK: true, Entries: entries})
}

func (s *Server) get(w io.Writer, id string) error {
	if !shelf.ValidID(id) {
		return writeResponse(w, response{Code: "bad_request", Error: "bad file id"})
	}
	f, e, err := s.Shelf.Open(id)
	if errors.Is(err, shelf.ErrNotFound) {
		return writeResponse(w, response{Code: "not_found", Error: "no such file"})
	}
	if err != nil {
		s.logf("transfer: open %s: %v", id, err)
		return writeResponse(w, response{Code: "internal", Error: "could not open the file"})
	}
	defer f.Close()
	if err := writeResponse(w, response{OK: true, Entry: &e}); err != nil {
		return err
	}
	n, err := io.CopyN(w, f, e.Size)
	if err != nil {
		return fmt.Errorf("send %s: sent %d of %d bytes: %w", id, n, e.Size, err)
	}
	return nil
}

// ---------------------------------------------------------------- client

// Client talks to other devices' servers.
type Client struct {
	Cert tls.Certificate
	// MaxSize is the largest file Pull accepts. Zero means no limit.
	MaxSize int64

	idle time.Duration // test hook
}

func (c *Client) idleTime() time.Duration {
	if c.idle > 0 {
		return c.idle
	}
	return idleTimeout
}

// dial connects to addr and checks that the device there is peer.
func (c *Client) dial(ctx context.Context, addr string, peer identity.DeviceID) (deadlineConn, *bufio.Reader, error) {
	cfg := identity.PinnedConfig(c.Cert, func(id identity.DeviceID) bool { return id == peer })
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: handshakeTimeout}, Config: cfg}
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	nc, err := d.DialContext(hctx, "tcp", addr)
	if err != nil {
		if errors.Is(err, identity.ErrUnknownPeer) {
			return deadlineConn{}, nil, fmt.Errorf("%w: %v", ErrWrongPeer, err)
		}
		return deadlineConn{}, nil, err
	}
	// Stop a blocked read or write when ctx ends. Close releases the watcher.
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	conn := deadlineConn{Conn: nc, idle: c.idleTime(), stop: stop}
	return conn, bufio.NewReaderSize(conn, 32*1024), nil
}

func (c *Client) roundTrip(ctx context.Context, addr string, peer identity.DeviceID, req request) (deadlineConn, *bufio.Reader, response, error) {
	conn, r, err := c.dial(ctx, addr, peer)
	if err != nil {
		return deadlineConn{}, nil, response{}, err
	}
	fail := func(err error) (deadlineConn, *bufio.Reader, response, error) {
		conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return deadlineConn{}, nil, response{}, err
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	line, err := readLine(r, maxResponseBytes)
	if err != nil {
		return fail(err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrBadResponse, err))
	}
	if !resp.OK {
		conn.Close()
		if resp.Code == "not_found" {
			return deadlineConn{}, nil, response{}, shelf.ErrNotFound
		}
		return deadlineConn{}, nil, response{}, &RemoteError{Message: cleanMessage(resp.Error)}
	}
	return conn, r, resp, nil
}

func cleanMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// List asks the device peer at addr for its files. Entries that do not look
// valid are dropped.
func (c *Client) List(ctx context.Context, addr string, peer identity.DeviceID) ([]shelf.Entry, error) {
	conn, _, resp, err := c.roundTrip(ctx, addr, peer, request{Op: "list"})
	if err != nil {
		return nil, err
	}
	conn.Close()
	var out []shelf.Entry
	for _, e := range resp.Entries {
		if len(out) >= maxListEntries {
			break
		}
		if e, ok := checkEntry(e); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// checkEntry cleans an entry received from another device and reports whether
// it is usable.
func checkEntry(e shelf.Entry) (shelf.Entry, bool) {
	if !shelf.ValidID(e.ID) || e.Size < 0 || !validHash(e.SHA256) {
		return e, false
	}
	e.Name = shelf.CleanName(e.Name)
	return e, true
}

func validHash(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

// Pull downloads file id from the device peer at addr into dir. The file is
// checked against its size and hash before it gets its final name. It returns
// the path and the file's description. An existing file is never overwritten:
// a number is added to the name instead.
func (c *Client) Pull(ctx context.Context, addr string, peer identity.DeviceID, id, dir string) (string, shelf.Entry, error) {
	if !shelf.ValidID(id) {
		return "", shelf.Entry{}, fmt.Errorf("transfer: %q is not a file id", id)
	}
	conn, r, resp, err := c.roundTrip(ctx, addr, peer, request{Op: "get", ID: id})
	if err != nil {
		return "", shelf.Entry{}, err
	}
	defer conn.Close()
	if resp.Entry == nil {
		return "", shelf.Entry{}, fmt.Errorf("%w: no file description", ErrBadResponse)
	}
	e, ok := checkEntry(*resp.Entry)
	if !ok || e.ID != id {
		return "", shelf.Entry{}, fmt.Errorf("%w: invalid file description", ErrBadResponse)
	}
	if c.MaxSize > 0 && e.Size > c.MaxSize {
		return "", shelf.Entry{}, ErrTooLarge
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", shelf.Entry{}, err
	}
	tmp, err := os.CreateTemp(dir, ".ephdrop-part-*")
	if err != nil {
		return "", shelf.Entry{}, err
	}
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.CopyN(io.MultiWriter(tmp, h), r, e.Size)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", shelf.Entry{}, fmt.Errorf("transfer: received %d of %d bytes: %w", n, e.Size, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return "", shelf.Entry{}, ErrBadFile
	}
	// Nothing may follow the file.
	if _, err := r.ReadByte(); err == nil {
		return "", shelf.Entry{}, fmt.Errorf("%w: extra data after the file", ErrBadFile)
	}
	if err := tmp.Sync(); err != nil {
		return "", shelf.Entry{}, err
	}
	if err := tmp.Close(); err != nil {
		return "", shelf.Entry{}, err
	}
	path, err := claimName(dir, e.Name)
	if err != nil {
		return "", shelf.Entry{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(path)
		return "", shelf.Entry{}, err
	}
	keep = true
	return path, e, nil
}

// claimName creates an empty file in dir with a name based on name that does
// not exist yet, and returns its path.
func claimName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // a name like ".bashrc"
		stem, ext = name, ""
	}
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = stem + " (" + strconv.Itoa(i) + ")" + ext
		}
		p := filepath.Join(dir, candidate)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("transfer: too many files with the same name")
}
