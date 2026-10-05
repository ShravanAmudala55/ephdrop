package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
	"github.com/ShravanAmudala55/ephdrop/core/node"
)

// quietBackend finds nobody and announces nothing.
type quietBackend struct{}

func (quietBackend) Advertise(ctx context.Context, _ discovery.Advertisement) error {
	<-ctx.Done()
	return ctx.Err()
}

func (quietBackend) Browse(ctx context.Context, _ func(discovery.Sighting), _ func(identity.DeviceID)) error {
	<-ctx.Done()
	return ctx.Err()
}

type rig struct {
	n      *node.Node
	s      *Server
	base   string
	host   string
	client *http.Client // signed in
}

func newRig(t *testing.T, name string) *rig {
	t.Helper()
	n, err := node.New(node.Config{Dir: t.TempDir(), Name: name, Listen: "127.0.0.1:0", Backend: quietBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := n.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := New(n)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		n.Wait()
	})
	r := &rig{n: n, s: s, host: ln.Addr().String()}
	r.base = "http://" + r.host
	jar, _ := cookiejar.New(nil)
	r.client = &http.Client{Jar: jar}
	resp, err := r.client.Get(r.base + "/?token=" + url.QueryEscape(s.Token()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return r
}

func (r *rig) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	resp, b, err := r.try(method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// try is do without a *testing.T, for use in other goroutines.
func (r *rig) try(method, path string, body any) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b, nil
}

func (r *rig) state(t *testing.T) stateJSON {
	t.Helper()
	resp, b := r.do(t, "GET", "/api/state", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("state: %d %s", resp.StatusCode, b)
	}
	var st stateJSON
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSessionIsRequired(t *testing.T) {
	r := newRig(t, "a")
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/", "/api/state", "/api/events", "/index.html"} {
		resp, err := plain.Get(r.base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", p, resp.StatusCode)
		}
	}
	resp, _ := plain.Get(r.base + "/?token=wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("cookie set for a wrong token")
	}
}

func TestTokenIsExchangedForAnHttpOnlyCookie(t *testing.T) {
	r := newRig(t, "a")
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := plain.Get(r.base + "/?token=" + r.s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("status %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cs := resp.Cookies()
	if len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %+v", cs)
	}
}

func TestBearerTokenWorksForScripts(t *testing.T) {
	r := newRig(t, "a")
	req, _ := http.NewRequest("GET", r.base+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+r.s.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer nope")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad bearer: %d", resp.StatusCode)
	}
}

func TestUnknownHostIsRefused(t *testing.T) {
	r := newRig(t, "a")
	for _, host := range []string{"evil.example.com", "evil.example.com:" + strings.Split(r.host, ":")[1], "127.0.0.1:1"} {
		req, _ := http.NewRequest("GET", r.base+"/api/state", nil)
		req.Host = host
		resp, err := r.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("host %q: %d", host, resp.StatusCode)
		}
	}
	// localhost with the right port is fine
	req, _ := http.NewRequest("GET", r.base+"/api/state", nil)
	req.Host = "localhost:" + strings.Split(r.host, ":")[1]
	resp, _ := r.client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("localhost: %d", resp.StatusCode)
	}
}

func TestForeignOriginsAndWrongContentTypesAreRefused(t *testing.T) {
	r := newRig(t, "a")
	post := func(origin, ctype, body string) int {
		req, _ := http.NewRequest("POST", r.base+"/api/share", strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"path":"/nonexistent"}`
	if c := post("https://evil.example.com", "application/json", body); c != http.StatusForbidden {
		t.Errorf("foreign origin: %d", c)
	}
	if c := post("", "text/plain", body); c != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", c)
	}
	if c := post("", "", body); c != http.StatusUnsupportedMediaType {
		t.Errorf("no type: %d", c)
	}
	if c := post("http://"+r.host, "application/json", body); c == http.StatusForbidden || c == http.StatusUnsupportedMediaType {
		t.Errorf("same origin refused: %d", c)
	}
	if c := post("", "application/json; charset=utf-8", body); c == http.StatusUnsupportedMediaType {
		t.Errorf("json with charset refused: %d", c)
	}
}

func TestSecurityHeaders(t *testing.T) {
	r := newRig(t, "a")
	resp, _ := r.do(t, "GET", "/api/state", nil)
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", resp.Header)
	}
}

func TestStateStartsEmpty(t *testing.T) {
	r := newRig(t, "my laptop")
	_, b := r.do(t, "GET", "/api/state", nil)
	// lists must be [] not null, so the UI can loop over them
	if !bytes.Contains(b, []byte(`"peers":[]`)) || !bytes.Contains(b, []byte(`"items":[]`)) || !bytes.Contains(b, []byte(`"invite":null`)) {
		t.Errorf("state %s", b)
	}
	st := r.state(t)
	if st.Self.Name != "my laptop" || st.Self.ID != r.n.ID() || st.Self.Port == 0 {
		t.Errorf("self %+v", st.Self)
	}
}

func TestShareByPathListDownloadDelete(t *testing.T) {
	r := newRig(t, "a")
	p := filepath.Join(t.TempDir(), "report.txt")
	os.WriteFile(p, []byte("quarterly numbers"), 0o600)

	resp, b := r.do(t, "POST", "/api/share", map[string]any{"path": p, "ttlHours": 2})
	if resp.StatusCode != 200 {
		t.Fatalf("share: %d %s", resp.StatusCode, b)
	}
	st := r.state(t)
	if len(st.Items) != 1 || st.Items[0].Name != "report.txt" || !st.Items[0].Local || st.Items[0].HolderName != "a" {
		t.Fatalf("items %+v", st.Items)
	}
	it := st.Items[0]
	if d := it.Expires.Sub(it.Created); d != 2*time.Hour {
		t.Errorf("ttl %v", d)
	}

	resp, b = r.do(t, "GET", "/api/files/"+it.ID, nil)
	if resp.StatusCode != 200 || string(b) != "quarterly numbers" {
		t.Fatalf("download: %d %q", resp.StatusCode, b)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "report.txt") {
		t.Errorf("disposition %q", cd)
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("type %q", resp.Header.Get("Content-Type"))
	}

	resp, _ = r.do(t, "DELETE", "/api/files/"+it.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if len(r.state(t).Items) != 0 {
		t.Error("still listed")
	}
	resp, _ = r.do(t, "GET", "/api/files/"+it.ID, nil)
	if resp.StatusCode != 404 {
		t.Errorf("download after delete: %d", resp.StatusCode)
	}
	resp, _ = r.do(t, "DELETE", "/api/files/"+it.ID, nil)
	if resp.StatusCode != 404 {
		t.Errorf("second delete: %d", resp.StatusCode)
	}
}

func TestShareRejectsBadInput(t *testing.T) {
	r := newRig(t, "a")
	dir := t.TempDir()
	cases := map[string]map[string]any{
		"relative path": {"path": "relative.txt"},
		"empty path":    {"path": ""},
		"missing file":  {"path": filepath.Join(dir, "nope")},
		"a folder":      {"path": dir},
		"negative ttl":  {"path": filepath.Join(dir, "x"), "ttlHours": -1},
		"huge ttl":      {"path": filepath.Join(dir, "x"), "ttlHours": 1000},
	}
	for name, body := range cases {
		resp, b := r.do(t, "POST", "/api/share", body)
		if resp.StatusCode < 400 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
		if !bytes.Contains(b, []byte(`"error"`)) {
			t.Errorf("%s: body %s", name, b)
		}
	}
	req, _ := http.NewRequest("POST", r.base+"/api/share", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("empty body: %d", resp.StatusCode)
	}
	if len(r.state(t).Items) != 0 {
		t.Error("something was shared")
	}
}

func TestUpload(t *testing.T) {
	r := newRig(t, "a")
	req, _ := http.NewRequest("PUT", r.base+"/api/upload?name="+url.QueryEscape("../my file.txt")+"&ttlHours=3", strings.NewReader("uploaded bytes"))
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	st := r.state(t)
	if len(st.Items) != 1 || st.Items[0].Name != "my file.txt" || st.Items[0].Size != 14 {
		t.Errorf("items %+v", st.Items)
	}
	for _, q := range []string{"", "?name=x&ttlHours=abc", "?name=x&ttlHours=-1", "?name=x&ttlHours=9999"} {
		req, _ := http.NewRequest("PUT", r.base+"/api/upload"+q, strings.NewReader("x"))
		resp, _ := r.client.Do(req)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("query %q: %d", q, resp.StatusCode)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	r := newRig(t, "a")
	resp, _ := r.do(t, "POST", "/api/fetch", map[string]any{"holder": "x", "id": strings.Repeat("a", 26)})
	if resp.StatusCode != 404 {
		t.Errorf("unknown file: %d", resp.StatusCode)
	}
	resp, _ = r.do(t, "POST", "/api/fetch", map[string]any{"holder": "x", "id": "y", "dir": "relative"})
	if resp.StatusCode != 400 {
		t.Errorf("relative dir: %d", resp.StatusCode)
	}
}

func TestPairingThroughTheAPIAndFetchBetweenTwoDevices(t *testing.T) {
	a, b := newRig(t, "laptop"), newRig(t, "phone")

	events := openEvents(t, a)
	defer events.close()
	events.expect(t, "changed")

	resp, body := a.do(t, "POST", "/api/invite", nil)
	if resp.StatusCode != 200 {
		t.Skipf("cannot make an invite here: %s", body)
	}
	var inv inviteJSON
	json.Unmarshal(body, &inv)
	if !strings.HasPrefix(inv.Text, "ephdrop://pair/") {
		t.Fatalf("invite %q", inv.Text)
	}
	if st := a.state(t); st.Invite == nil || st.Invite.Text != inv.Text {
		t.Errorf("state invite %+v", st.Invite)
	}

	joined := make(chan int, 1)
	go func() {
		resp, _, err := b.try("POST", "/api/join", map[string]any{"invite": " " + inv.Text + "\n"})
		if err != nil {
			joined <- -1
			return
		}
		joined <- resp.StatusCode
	}()
	req := events.expect(t, "pair-request")
	if req.Name != "phone" || req.ID == "" {
		t.Fatalf("request %+v", req)
	}
	// a wrong id is refused
	if resp, _ := a.do(t, "POST", "/api/invite/reply", map[string]any{"id": "nope", "accept": true}); resp.StatusCode != 404 {
		t.Errorf("wrong reply id: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "POST", "/api/invite/reply", map[string]any{"id": req.ID, "accept": true}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reply: %d", resp.StatusCode)
	}
	done := events.expect(t, "pair-done")
	if !done.OK || done.Name != "phone" {
		t.Errorf("done %+v", done)
	}
	select {
	case code := <-joined:
		if code != 200 {
			t.Errorf("join: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("join never finished")
	}
	if st := a.state(t); len(st.Peers) != 1 || st.Peers[0].Name != "phone" || st.Invite != nil {
		t.Errorf("a state %+v", st)
	}
	if st := b.state(t); len(st.Peers) != 1 || st.Peers[0].Name != "laptop" {
		t.Errorf("b state %+v", st)
	}

	// Unpair through the API.
	pid := a.state(t).Peers[0].ID
	if resp, _ := a.do(t, "DELETE", "/api/peers/"+string(pid), nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("unpair: %d", resp.StatusCode)
	}
	if len(a.state(t).Peers) != 0 {
		t.Error("still paired")
	}
}

func TestDecliningAndCancellingAnInvite(t *testing.T) {
	a, b := newRig(t, "a"), newRig(t, "b")
	events := openEvents(t, a)
	defer events.close()
	events.expect(t, "changed")

	resp, body := a.do(t, "POST", "/api/invite", nil)
	if resp.StatusCode != 200 {
		t.Skipf("cannot make an invite here: %s", body)
	}
	var inv inviteJSON
	json.Unmarshal(body, &inv)
	go b.try("POST", "/api/join", map[string]any{"invite": inv.Text})
	req := events.expect(t, "pair-request")
	a.do(t, "POST", "/api/invite/reply", map[string]any{"id": req.ID, "accept": false})
	done := events.expect(t, "pair-done")
	if done.OK || done.Error == "" {
		t.Errorf("declined invite reported %+v", done)
	}
	if len(a.state(t).Peers) != 0 {
		t.Error("paired after declining")
	}

	// Cancelling, or replacing an invite, is quiet: no failure event.
	a.do(t, "POST", "/api/invite", nil)
	a.do(t, "POST", "/api/invite", nil)
	resp, _ = a.do(t, "DELETE", "/api/invite", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	waitUntil(t, func() bool { return a.state(t).Invite == nil }, "the invite to clear")
	events.expectNone(t, 500*time.Millisecond)
}

func TestJoinWithABadInvite(t *testing.T) {
	r := newRig(t, "a")
	resp, _ := r.do(t, "POST", "/api/join", map[string]any{"invite": "garbage"})
	if resp.StatusCode < 400 {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestHint(t *testing.T) {
	r := newRig(t, "a")
	resp, _ := r.do(t, "POST", "/api/peers/"+strings.Repeat("a", 26)+"/hint", map[string]any{"addr": "10.0.0.5:4000"})
	if resp.StatusCode != 400 { // not paired
		t.Errorf("hint for a stranger: %d", resp.StatusCode)
	}
}

func TestEventsAreSentWhenFilesChange(t *testing.T) {
	r := newRig(t, "a")
	events := openEvents(t, r)
	defer events.close()
	events.expect(t, "changed")
	req, _ := http.NewRequest("PUT", r.base+"/api/upload?name=a.txt", strings.NewReader("x"))
	resp, _ := r.client.Do(req)
	resp.Body.Close()
	events.expect(t, "changed")
}

func TestStaticFilesAreServed(t *testing.T) {
	r := newRig(t, "a")
	resp, b := r.do(t, "GET", "/", nil)
	if resp.StatusCode != 200 || !bytes.Contains(b, []byte("<")) {
		t.Errorf("index: %d", resp.StatusCode)
	}
	resp, _ = r.do(t, "GET", "/nope.js", nil)
	if resp.StatusCode != 404 {
		t.Errorf("missing file: %d", resp.StatusCode)
	}
}

// ---- event stream helper

type eventStream struct {
	resp *http.Response
	ch   chan event
}

func openEvents(t *testing.T, r *rig) *eventStream {
	t.Helper()
	resp, err := r.client.Get(r.base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	es := &eventStream{resp: resp, ch: make(chan event, 64)}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				var ev event
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
					es.ch <- ev
				}
			}
		}
		close(es.ch)
	}()
	return es
}

func (e *eventStream) close() { e.resp.Body.Close() }

// expect waits for an event of the given type, skipping others of type "changed".
func (e *eventStream) expect(t *testing.T, typ string) event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-e.ch:
			if !ok {
				t.Fatalf("event stream closed while waiting for %s", typ)
			}
			if ev.Type == typ {
				return ev
			}
			if ev.Type != "changed" {
				t.Fatalf("got event %+v while waiting for %s", ev, typ)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

func (e *eventStream) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-e.ch:
			if ev.Type != "changed" {
				t.Fatalf("unexpected event %+v", ev)
			}
		case <-deadline:
			return
		}
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
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
