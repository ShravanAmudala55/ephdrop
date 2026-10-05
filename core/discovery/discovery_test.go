package discovery

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

func newID(t *testing.T) identity.DeviceID {
	t.Helper()
	i, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return i.ID()
}

func ips(t *testing.T, addrs ...string) []netip.Addr {
	t.Helper()
	var out []netip.Addr
	for _, a := range addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestAdvertisementRoundTrip(t *testing.T) {
	id := newID(t)
	ad := Advertisement{ID: id, Port: 4321}
	if err := ad.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := ParseTXT(ad.TXT())
	if err != nil || got != id {
		t.Fatalf("ParseTXT = %q, %v", got, err)
	}
	if name := ad.InstanceName(); name != "ephdrop-"+string(id)[:12] || len(name) > 63 {
		t.Fatalf("InstanceName = %q", name)
	}
}

func TestAdvertisementValidate(t *testing.T) {
	id := newID(t)
	for name, ad := range map[string]Advertisement{
		"empty id":     {ID: "", Port: 80},
		"short id":     {ID: "abc", Port: 80},
		"upper id":     {ID: identity.DeviceID(strings.ToUpper(string(id))), Port: 80},
		"port zero":    {ID: id, Port: 0},
		"port too big": {ID: id, Port: 65536},
	} {
		if err := ad.Validate(); !errors.Is(err, ErrBadAdvertisement) {
			t.Errorf("%s: want ErrBadAdvertisement, got %v", name, err)
		}
	}
}

func TestParseTXT(t *testing.T) {
	id := string(newID(t))
	ok := func(txt ...string) {
		t.Helper()
		got, err := ParseTXT(txt)
		if err != nil || string(got) != id {
			t.Errorf("ParseTXT(%q) = %q, %v", txt, got, err)
		}
	}
	ok("v=1", "id="+id)
	ok("id="+id, "v=1")
	ok("V=1", "ID="+id)                    // keys are case-insensitive
	ok("v=1", "id="+id, "id=zzz", "v=9")   // first entry for a key wins
	ok("flag", "v=1", "other=x", "id="+id) // unrelated entries are ignored
	ok("v=1", "id="+id, "note=a=b=c")      // values may contain '='

	bad := map[string][]string{
		"no version":      {"id=" + id},
		"no id":           {"v=1"},
		"empty":           nil,
		"bad version":     {"v=one", "id=" + id},
		"bad id":          {"v=1", "id=not-a-valid-id"},
		"id upper case":   {"v=1", "id=" + strings.ToUpper(id)},
		"id too long":     {"v=1", "id=" + id + "a"},
		"key without eq":  {"v", "id" + id},
		"oversized entry": {"v=1", "id=" + id, "x=" + strings.Repeat("a", 300)},
	}
	for name, txt := range bad {
		if _, err := ParseTXT(txt); !errors.Is(err, ErrBadAdvertisement) {
			t.Errorf("%s: want ErrBadAdvertisement, got %v", name, err)
		}
	}
}

func TestParseTXTNewerVersion(t *testing.T) {
	id := newID(t)
	_, err := ParseTXT([]string{"v=2", "id=" + string(id)})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("want ErrUnsupportedVersion, got %v", err)
	}
}

func TestSightingFromService(t *testing.T) {
	id := newID(t)
	txt := Advertisement{ID: id, Port: 1}.TXT()

	s, err := SightingFromService(txt, 4000, ips(t, "192.168.1.20", "fe80::1%wlan0", "10.0.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.5:4000", "192.168.1.20:4000", "[fe80::1%wlan0]:4000"}
	if s.ID != id || strings.Join(s.Addrs, " ") != strings.Join(want, " ") {
		t.Fatalf("got %+v, want addrs %v", s, want)
	}
}

func TestSightingFromServiceDropsNonLocalAddresses(t *testing.T) {
	txt := Advertisement{ID: newID(t), Port: 1}.TXT()
	s, err := SightingFromService(txt, 80, ips(t,
		"8.8.8.8",            // public
		"2001:db8::1",        // public IPv6
		"0.0.0.0",            // unspecified
		"224.0.0.251",        // multicast
		"::ffff:192.168.0.9", // IPv4 mapped, local
		"127.0.0.1",          // loopback
		"fd12:3456::1",       // unique local
		"192.168.0.9",        // duplicate of the mapped one
	))
	if err != nil {
		t.Fatal(err)
	}
	want := "127.0.0.1:80 192.168.0.9:80 [fd12:3456::1]:80"
	if got := strings.Join(s.Addrs, " "); got != want {
		t.Fatalf("addrs = %q, want %q", got, want)
	}
}

func TestSightingFromServiceErrors(t *testing.T) {
	txt := Advertisement{ID: newID(t), Port: 1}.TXT()
	if _, err := SightingFromService(txt, 80, ips(t, "8.8.8.8")); !errors.Is(err, ErrNoUsableAddress) {
		t.Errorf("only public addresses: want ErrNoUsableAddress, got %v", err)
	}
	if _, err := SightingFromService(txt, 80, nil); !errors.Is(err, ErrNoUsableAddress) {
		t.Errorf("no addresses: want ErrNoUsableAddress, got %v", err)
	}
	if _, err := SightingFromService(txt, 0, ips(t, "10.0.0.1")); !errors.Is(err, ErrBadAdvertisement) {
		t.Errorf("port 0: want ErrBadAdvertisement, got %v", err)
	}
	if _, err := SightingFromService([]string{"v=1"}, 80, ips(t, "10.0.0.1")); !errors.Is(err, ErrBadAdvertisement) {
		t.Errorf("bad TXT: want ErrBadAdvertisement, got %v", err)
	}
}

func TestSightingFromServiceCapsAddresses(t *testing.T) {
	txt := Advertisement{ID: newID(t), Port: 1}.TXT()
	var many []netip.Addr
	for i := 1; i <= 30; i++ {
		many = append(many, netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}))
	}
	s, err := SightingFromService(txt, 80, many)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Addrs) != maxAddrs {
		t.Fatalf("got %d addresses, want %d", len(s.Addrs), maxAddrs)
	}
}

func TestParseAddr(t *testing.T) {
	for _, s := range []string{"192.168.1.5:80", "[fd00::1]:80", "[::ffff:10.0.0.1]:80", "127.0.0.1:1"} {
		if _, err := parseAddr(s, true); err != nil {
			t.Errorf("strict %q: %v", s, err)
		}
	}
	for _, s := range []string{
		"", "nonsense", "host.local:80", "192.168.1.5", "192.168.1.5:0", "192.168.1.5:99999",
		"0.0.0.0:80", "224.0.0.1:80", "8.8.8.8:80", "[2001:db8::1]:80",
	} {
		if _, err := parseAddr(s, true); err == nil {
			t.Errorf("strict %q: expected an error", s)
		}
	}
	// Non-strict mode allows other IP addresses, such as a VPN address.
	for _, s := range []string{"8.8.8.8:80", "100.64.1.2:4000", "[2001:db8::1]:80"} {
		if _, err := parseAddr(s, false); err != nil {
			t.Errorf("non-strict %q: %v", s, err)
		}
	}
	if _, err := parseAddr("host.local:80", false); err == nil {
		t.Error("non-strict: hostnames must still be refused")
	}
	if _, err := parseAddr("0.0.0.0:80", false); err == nil {
		t.Error("non-strict: unspecified address must still be refused")
	}
}

func TestAddressesAreNormalized(t *testing.T) {
	ap, err := parseAddr("[::ffff:10.0.0.1]:80", true)
	if err != nil {
		t.Fatal(err)
	}
	if ap.String() != "10.0.0.1:80" {
		t.Fatalf("got %q, want the plain IPv4 form", ap)
	}
	got := cleanAddrs([]string{"[::ffff:10.0.0.1]:80", "10.0.0.1:80"}, true)
	if len(got) != 1 || got[0] != "10.0.0.1:80" {
		t.Fatalf("mapped and plain forms should merge, got %v", got)
	}
}

func TestTidyOrdersAndDedupes(t *testing.T) {
	got := tidy([]string{"[fd00::1]:80", "10.0.0.2:80", "10.0.0.1:80", "10.0.0.2:80", "[fd00::0]:80"})
	want := "10.0.0.1:80 10.0.0.2:80 [fd00::0]:80 [fd00::1]:80"
	if strings.Join(got, " ") != want {
		t.Fatalf("tidy = %v", got)
	}
}
