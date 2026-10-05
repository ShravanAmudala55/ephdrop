package pairing

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

func newIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setNow(t *testing.T, f func() time.Time) {
	t.Helper()
	old := now
	now = f
	t.Cleanup(func() { now = old })
}

func TestInviteRoundTrip(t *testing.T) {
	self := newIdentity(t)
	inv, err := NewInvite(self, []string{"192.168.1.5:4000", "10.0.0.2:4000"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseInvite(inv.String())
	if err != nil {
		t.Fatalf("ParseInvite: %v", err)
	}
	if got.ID() != self.ID() {
		t.Fatal("id changed")
	}
	if string(got.Secret) != string(inv.Secret) || !got.Expires.Equal(inv.Expires) {
		t.Fatal("secret or expiry changed")
	}
	if strings.Join(got.Addrs, ",") != "192.168.1.5:4000,10.0.0.2:4000" {
		t.Fatalf("addrs = %v", got.Addrs)
	}
}

func TestInviteIsUniquePerCall(t *testing.T) {
	self := newIdentity(t)
	a, _ := NewInvite(self, []string{"1.2.3.4:5"}, time.Minute)
	b, _ := NewInvite(self, []string{"1.2.3.4:5"}, time.Minute)
	if string(a.Secret) == string(b.Secret) {
		t.Fatal("two invites share a secret")
	}
}

func TestNewInviteValidation(t *testing.T) {
	self := newIdentity(t)
	cases := map[string]struct {
		addrs []string
		ttl   time.Duration
	}{
		"no addresses":   {nil, time.Minute},
		"bad address":    {[]string{"nonsense"}, time.Minute},
		"empty host":     {[]string{":80"}, time.Minute},
		"port zero":      {[]string{"1.2.3.4:0"}, time.Minute},
		"port too large": {[]string{"1.2.3.4:70000"}, time.Minute},
		"too many":       {[]string{"1.1.1.1:1", "1.1.1.2:1", "1.1.1.3:1", "1.1.1.4:1", "1.1.1.5:1", "1.1.1.6:1", "1.1.1.7:1", "1.1.1.8:1", "1.1.1.9:1"}, time.Minute},
		"ttl too short":  {[]string{"1.2.3.4:5"}, time.Millisecond},
	}
	for name, c := range cases {
		if _, err := NewInvite(self, c.addrs, c.ttl); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseInviteRejectsBadInput(t *testing.T) {
	self := newIdentity(t)
	good, _ := NewInvite(self, []string{"1.2.3.4:5"}, time.Minute)
	body := strings.TrimPrefix(good.String(), invitePrefix)

	for name, s := range map[string]string{
		"empty":        "",
		"wrong scheme": "https://example.com/" + body,
		"bad base64":   invitePrefix + "!!!",
		"not json":     invitePrefix + "bm90IGpzb24",
		"truncated":    invitePrefix + body[:len(body)/2],
	} {
		if _, err := ParseInvite(s); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("%s: want ErrInvalidInvite, got %v", name, err)
		}
	}
}

func TestParseInviteRejectsExpired(t *testing.T) {
	self := newIdentity(t)
	inv, _ := NewInvite(self, []string{"1.2.3.4:5"}, time.Minute)
	setNow(t, func() time.Time { return time.Now().Add(2 * time.Minute) })
	if _, err := ParseInvite(inv.String()); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("want ErrInviteExpired, got %v", err)
	}
}

func TestParseInviteTrimsWhitespace(t *testing.T) {
	inv, _ := NewInvite(newIdentity(t), []string{"1.2.3.4:5"}, time.Minute)
	if _, err := ParseInvite("  " + inv.String() + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAddrsFormat(t *testing.T) {
	for _, a := range LocalAddrs(1234) {
		if !strings.HasSuffix(a, ":1234") || strings.HasPrefix(a, "127.") {
			t.Errorf("unexpected address %q", a)
		}
	}
}
