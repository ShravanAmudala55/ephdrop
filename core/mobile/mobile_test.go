package mobile

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeNative struct{ ips string }

func (f fakeNative) LocalIPs() string { return f.ips }

func myIP(t *testing.T) string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip := n.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	t.Skip("no network address on this machine")
	return ""
}

func startAgent(t *testing.T, name, ips string) *Agent {
	t.Helper()
	a, err := Start(t.TempDir(), name, t.TempDir(), fakeNative{ips})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	return a
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Two phones pair, the app reports each to the other (as NSD or Bonjour would),
// and a file shared on one is listed and fetched on the other.
func TestTwoAgentsPairAndShare(t *testing.T) {
	ip := myIP(t)
	a := startAgent(t, "phone-a", ip)
	b := startAgent(t, "phone-b", ip)

	sess, err := a.node.StartInvite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sess.Text, "ephdrop://pair/") {
		t.Fatalf("invite %q", sess.Text)
	}
	go func() {
		req := <-sess.Requests
		req.Reply(true)
	}()
	if _, err := b.node.Join(t.Context(), sess.Text); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pairing on both", func() bool { return len(a.node.Peers()) == 1 && len(b.node.Peers()) == 1 })

	// The app hears each device and says so. A hears B before B has been heard
	// by anyone else; B hears A twice, which is normal.
	if err := a.Seen(b.ID(), "127.0.0.1", b.TransferPort(), "1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := b.Seen(a.ID(), "127.0.0.1", a.TransferPort(), "1"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.node.Share(strings.NewReader("hello from a"), "hi.txt", time.Hour); err != nil {
		t.Fatal(err)
	}
	var id string
	waitFor(t, "b to list a's file", func() bool {
		for _, it := range b.node.Items() {
			if it.Name == "hi.txt" {
				id = it.ID
				return true
			}
		}
		return false
	})
	path, _, err := b.node.Fetch(t.Context(), a.node.ID(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := openFile(path)
	got, _ := io.ReadAll(f)
	if string(got) != "hello from a" {
		t.Fatalf("got %q", got)
	}
}

func TestReportsBeforeBrowseStartsAreKept(t *testing.T) {
	var b nativeBackend
	var s0 = sightingFor(t, "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	b.seen(s0)
	got := make(chan string, 1)
	ctx := t.Context()
	go b.Browse(ctx, func(s sighting) { got <- string(s.ID) }, func(id idType) {})
	select {
	case id := <-got:
		if id != "aaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatal(id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("early report was lost")
	}
}

func TestSeenRefusesBadAdvertisements(t *testing.T) {
	a := startAgent(t, "p", "10.0.0.5")
	if err := a.Seen("not an id", "10.0.0.6", 4000, "1"); err == nil {
		t.Error("bad id accepted")
	}
	if err := a.Seen(strings.Repeat("a", 26), "10.0.0.6", 4000, "99"); err == nil {
		t.Error("unknown version accepted")
	}
	if err := a.Seen(strings.Repeat("a", 26), "10.0.0.6", 0, "1"); err == nil {
		t.Error("port 0 accepted")
	}
	if err := a.Seen(a.ID(), "10.0.0.6", 4000, "1"); err != nil {
		t.Errorf("own advertisement should be ignored quietly: %v", err)
	}
}

func TestInviteUsesTheAddressesTheAppGives(t *testing.T) {
	a := startAgent(t, "p", "10.9.8.7, 169.254.1.1, 127.0.0.1, ::1, nonsense")
	sess, err := a.node.StartInvite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Cancel()
	raw := strings.TrimPrefix(sess.Text, "ephdrop://pair/")
	dec, err := decodeInvite(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec) != 1 || !strings.HasPrefix(dec[0], "10.9.8.7:") {
		t.Fatalf("invite addresses %v", dec)
	}
}

func TestInviteWithNoAddressesSaysSo(t *testing.T) {
	a := startAgent(t, "p", "")
	if _, err := a.node.StartInvite(t.Context()); err == nil {
		t.Fatal("expected an error with no network address")
	}
}

func TestStopTwiceAndPortsAreSet(t *testing.T) {
	a, err := Start(t.TempDir(), "p", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Port() == 0 || a.TransferPort() == 0 || len(a.ID()) != 26 || a.Token() == "" || !strings.Contains(a.URL(), "token="+a.Token()) {
		t.Fatalf("bad agent: %d %d %q", a.Port(), a.TransferPort(), a.ID())
	}
	a.Stop()
	a.Stop()
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(a.Port())), 300*time.Millisecond); err == nil {
		c.Close()
		t.Error("server still listening after Stop")
	}
}
