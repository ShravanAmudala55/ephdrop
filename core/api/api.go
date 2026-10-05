// Package api serves a node over HTTP on this computer only, for the desktop
// app's window and for scripts.
//
// Because any program (and any web page open in a browser) can try to talk to a
// local port, the server is strict:
//
//   - It only answers requests whose Host is the address it listens on, which
//     defeats DNS rebinding.
//   - It needs a session. The program that starts the server learns a random
//     token. Opening /?token=... exchanges it for an HttpOnly, SameSite=Strict
//     cookie. A script can send the token as "Authorization: Bearer ...".
//   - Requests that change something must be JSON (or a raw upload) and, when
//     the browser sends an Origin header, it must be the server itself.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/board"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/node"
	"github.com/ShravanAmudala55/ephdrop/core/shelf"
)

//go:embed ui
var uiFiles embed.FS

const (
	cookieName  = "ephdrop_session"
	maxJSONBody = 1 << 20
)

// Server serves one node.
type Server struct {
	node  *node.Node
	token string
	ui    fs.FS

	mu      sync.Mutex
	hosts   map[string]bool
	invite  *node.InviteSession
	stopped map[*node.InviteSession]bool // invites ended on purpose, so no failure is reported
	pending map[string]func(bool)        // pair request id -> reply
	subs    map[int]chan event
	nextSub int
}

type event struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// New creates a Server. The token is created here, see Token.
func New(n *node.Node) (*Server, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	return &Server{
		node: n, token: base64.RawURLEncoding.EncodeToString(b[:]), ui: ui,
		hosts: map[string]bool{}, stopped: map[*node.InviteSession]bool{}, pending: map[string]func(bool){}, subs: map[int]chan event{},
	}, nil
}

// Token is the secret the starting program uses to open a session.
func (s *Server) Token() string { return s.token }

// Serve answers requests on ln until ctx ends. ln should listen on a loopback
// address.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	addr := ln.Addr().(*net.TCPAddr)
	s.mu.Lock()
	s.hosts[net.JoinHostPort("127.0.0.1", strconv.Itoa(addr.Port))] = true
	s.hosts[net.JoinHostPort("localhost", strconv.Itoa(addr.Port))] = true
	s.hosts[net.JoinHostPort("::1", strconv.Itoa(addr.Port))] = true
	s.mu.Unlock()
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
		srv.Close()
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler returns the HTTP handler. Serve sets up which Host values are
// accepted. Tests that use their own listener call AllowHost first.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/events", s.getEvents)
	mux.HandleFunc("POST /api/share", s.postShare)
	mux.HandleFunc("PUT /api/upload", s.putUpload)
	mux.HandleFunc("GET /api/files/{id}", s.getFile)
	mux.HandleFunc("DELETE /api/files/{id}", s.deleteFile)
	mux.HandleFunc("POST /api/fetch", s.postFetch)
	mux.HandleFunc("POST /api/invite", s.postInvite)
	mux.HandleFunc("DELETE /api/invite", s.deleteInvite)
	mux.HandleFunc("POST /api/invite/reply", s.postInviteReply)
	mux.HandleFunc("POST /api/join", s.postJoin)
	mux.HandleFunc("DELETE /api/peers/{id}", s.deletePeer)
	mux.HandleFunc("POST /api/peers/{id}/hint", s.postHint)
	mux.Handle("/", http.FileServerFS(s.ui))
	return s.guard(mux)
}

// AllowHost accepts requests whose Host header is host (for example
// "127.0.0.1:8080"). Serve does this for its own listener.
func (s *Server) AllowHost(host string) {
	s.mu.Lock()
	s.hosts[host] = true
	s.mu.Unlock()
}

// ---------------------------------------------------------------- guard

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")

		s.mu.Lock()
		okHost := s.hosts[r.Host]
		s.mu.Unlock()
		if !okHost {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		// Exchange the token for a session cookie.
		if r.Method == http.MethodGet && r.URL.Path == "/" && r.URL.Query().Get("token") != "" {
			if !s.tokenOK(r.URL.Query().Get("token")) {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "not signed in. Open the app to start a session.", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					http.Error(w, "bad origin", http.StatusForbidden)
					return
				}
			}
			if r.Method != http.MethodPut && r.Method != http.MethodDelete && !isJSON(r) {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func (s *Server) tokenOK(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && s.tokenOK(c.Value) {
		return true
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") && s.tokenOK(strings.TrimPrefix(a, "Bearer ")) {
		return true
	}
	return false
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, errors.New("unreadable request"))
		return false
	}
	return true
}

func ttlOf(hours float64) (time.Duration, error) {
	if hours < 0 || hours > shelf.MaxTTL.Hours() {
		return 0, shelf.ErrBadTTL
	}
	return time.Duration(hours * float64(time.Hour)), nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, shelf.ErrNotFound), errors.Is(err, board.ErrUnknownFile):
		return http.StatusNotFound
	case errors.Is(err, board.ErrUnreachable), errors.Is(err, node.ErrNotRunning):
		return http.StatusConflict
	case errors.Is(err, shelf.ErrBadTTL):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// ---------------------------------------------------------------- state

type selfJSON struct {
	ID          identity.DeviceID `json:"id"`
	Name        string            `json:"name"`
	Port        int               `json:"port"`
	DownloadDir string            `json:"downloadDir"`
}

type peerJSON struct {
	ID     identity.DeviceID `json:"id"`
	Name   string            `json:"name"`
	Online bool              `json:"online"`
	Added  time.Time         `json:"added"`
}

type itemJSON struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Size       int64             `json:"size"`
	SHA256     string            `json:"sha256"`
	Holder     identity.DeviceID `json:"holder"`
	HolderName string            `json:"holderName"`
	Local      bool              `json:"local"`
	Reachable  bool              `json:"reachable"`
	Created    time.Time         `json:"created"`
	Expires    time.Time         `json:"expires"`
}

type inviteJSON struct {
	Text    string    `json:"text"`
	Expires time.Time `json:"expires"`
}

type stateJSON struct {
	Self   selfJSON    `json:"self"`
	Peers  []peerJSON  `json:"peers"`
	Items  []itemJSON  `json:"items"`
	Invite *inviteJSON `json:"invite"`
}

func (s *Server) state() stateJSON {
	st := stateJSON{
		Self:  selfJSON{ID: s.node.ID(), Name: s.node.Name(), Port: s.node.Port(), DownloadDir: s.node.DownloadDir()},
		Peers: []peerJSON{},
		Items: []itemJSON{},
	}
	names := map[identity.DeviceID]string{s.node.ID(): s.node.Name()}
	for _, p := range s.node.Peers() {
		st.Peers = append(st.Peers, peerJSON{ID: p.ID, Name: p.Name, Online: p.Online, Added: p.Added})
		names[p.ID] = p.Name
	}
	for _, it := range s.node.Items() {
		st.Items = append(st.Items, itemJSON{
			ID: it.ID, Name: it.Name, Size: it.Size, SHA256: it.SHA256,
			Holder: it.Holder, HolderName: names[it.Holder], Local: it.Local, Reachable: it.Reachable,
			Created: it.Created, Expires: it.Expires,
		})
	}
	s.mu.Lock()
	if s.invite != nil {
		st.Invite = &inviteJSON{Text: s.invite.Text, Expires: s.invite.Expires}
	}
	s.mu.Unlock()
	return st
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state())
}

// ---------------------------------------------------------------- events

func (s *Server) subscribe() (<-chan event, func()) {
	ch := make(chan event, 16)
	s.mu.Lock()
	k := s.nextSub
	s.nextSub++
	s.subs[k] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, k)
		s.mu.Unlock()
	}
}

func (s *Server) broadcast(ev event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default: // a slow reader misses it, and will refresh from /api/state
		}
	}
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	changes, stopNode := s.node.Subscribe()
	defer stopNode()
	evs, stop := s.subscribe()
	defer stop()
	send := func(ev event) bool {
		b, _ := json.Marshal(ev)
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		return err == nil
	}
	if !send(event{Type: "changed"}) {
		return
	}
	beat := time.NewTicker(25 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send(event{Type: "changed"}) {
				return
			}
		case ev := <-evs:
			if !send(ev) {
				return
			}
		case <-beat.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// ---------------------------------------------------------------- files

func (s *Server) postShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string  `json:"path"`
		TTLHours float64 `json:"ttlHours"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !filepath.IsAbs(req.Path) {
		fail(w, http.StatusBadRequest, errors.New("path must be absolute"))
		return
	}
	ttl, err := ttlOf(req.TTLHours)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.node.ShareFile(req.Path, ttl)
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// putUpload shares the request body as a file. It is for browsers, which cannot
// hand over a file path.
func (s *Server) putUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		fail(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	hours := 0.0
	if v := r.URL.Query().Get("ttlHours"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			fail(w, http.StatusBadRequest, errors.New("bad ttlHours"))
			return
		}
		hours = f
	}
	ttl, err := ttlOf(hours)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.node.Share(r.Body, name, ttl)
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	f, e, err := s.node.Local(r.PathValue("id"))
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	io.Copy(w, f)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	if err := s.node.Remove(r.PathValue("id")); err != nil {
		fail(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Holder identity.DeviceID `json:"holder"`
		ID     string            `json:"id"`
		Dir    string            `json:"dir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Dir != "" && !filepath.IsAbs(req.Dir) {
		fail(w, http.StatusBadRequest, errors.New("dir must be absolute"))
		return
	}
	path, e, err := s.node.Fetch(r.Context(), req.Holder, req.ID, req.Dir)
	if err != nil {
		fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "entry": e})
}

// ---------------------------------------------------------------- pairing

func (s *Server) postInvite(w http.ResponseWriter, r *http.Request) {
	s.cancelInvite() // a new invite replaces the old one
	// The invite outlives this request.
	sess, err := s.node.StartInvite(context.Background())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	s.invite = sess
	s.mu.Unlock()
	go s.watchInvite(sess)
	writeJSON(w, http.StatusOK, inviteJSON{Text: sess.Text, Expires: sess.Expires})
}

func (s *Server) watchInvite(sess *node.InviteSession) {
	for {
		select {
		case req := <-sess.Requests:
			id := randomID()
			s.mu.Lock()
			s.pending[id] = req.Reply
			s.mu.Unlock()
			s.broadcast(event{Type: "pair-request", ID: id, Name: req.Peer.Name})
		case res, ok := <-sess.Done:
			if !ok {
				return
			}
			s.mu.Lock()
			quiet := s.stopped[sess]
			delete(s.stopped, sess)
			if s.invite == sess {
				s.invite = nil
			}
			for k, reply := range s.pending {
				reply(false)
				delete(s.pending, k)
			}
			s.mu.Unlock()
			if quiet {
				return
			}
			ev := event{Type: "pair-done", OK: res.Err == nil, Name: res.Peer.Name}
			if res.Err != nil {
				ev.Error = res.Err.Error()
			}
			s.broadcast(ev)
			return
		}
	}
}

// cancelInvite ends the current invite, if any, without reporting a failure.
func (s *Server) cancelInvite() {
	s.mu.Lock()
	sess := s.invite
	if sess != nil {
		s.stopped[sess] = true
	}
	s.mu.Unlock()
	if sess != nil {
		sess.Cancel()
	}
}

func randomID() string {
	var b [9]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request) {
	s.cancelInvite()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postInviteReply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Accept bool   `json:"accept"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	reply, ok := s.pending[req.ID]
	delete(s.pending, req.ID)
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, errors.New("no such request"))
		return
	}
	reply(req.Accept)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Invite string `json:"invite"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	peer, err := s.node.Join(r.Context(), strings.TrimSpace(req.Invite))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": peer.ID, "name": peer.Name})
}

func (s *Server) deletePeer(w http.ResponseWriter, r *http.Request) {
	if err := s.node.Unpair(identity.DeviceID(r.PathValue("id"))); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postHint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var addrs []string
	if req.Addr != "" {
		addrs = []string{req.Addr}
	}
	if err := s.node.Hint(identity.DeviceID(r.PathValue("id")), addrs...); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
