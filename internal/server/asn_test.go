package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestIPverse(t *testing.T) {
	input := `{"asn":212387,"prefixes":{"ipv4":["116.193.158.0/24"],"ipv6":["2a0f:b240:a5::/48"]}}`
	p, err := parseIPverse([]byte(input), "212387")
	if err != nil || len(p) != 2 {
		t.Fatalf("%v %v", p, err)
	}
	if containsPrefix(p, netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("unlisted prefix allowed")
	}
	if !containsPrefix(p, netip.MustParseAddr("116.193.158.22")) {
		t.Fatal("current prefix missing")
	}
	if _, err := parseIPverse([]byte(input), "123"); err == nil {
		t.Fatal("ASN mismatch accepted")
	}
	if _, err := parseIPverse([]byte(`{"asn":123,"prefixes":{"ipv4":["0.0.0.0/0"]}}`), "123"); err == nil {
		t.Fatal("default route accepted")
	}
	f := &asnFilter{cache: t.TempDir(), allowed: map[string]bool{"212387": true}}
	if err := os.WriteFile(filepath.Join(f.cache, "asn-ipverse-212387.json"), []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if len(f.prefixes) != 2 {
		t.Fatal("cache not loaded")
	}
}

func TestASNFilter(t *testing.T) {
	f := &asnFilter{allowed: map[string]bool{"13335": true}, proxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	ps, e := parseIPverse([]byte(`{"asn":13335,"prefixes":{"ipv4":["192.0.2.0/24"],"ipv6":["2001:db8::/32"]}}`), "13335")
	if e != nil {
		t.Fatal(e)
	}
	f.prefixes = ps
	s := &Server{asn: f}
	h := s.asnGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct {
		peer, xff string
		want      int
	}{
		{"192.0.2.20:90", "", 204},
		{"[2001:db8::1]:90", "", 204},
		{"198.51.100.1:90", "192.0.2.20", 403}, // spoofed header from direct visitor
		{"10.0.0.2:90", "192.0.2.20", 204},
		{"10.0.0.2:90", "192.0.2.20, 198.51.100.1", 403}, // spoofed leftmost hop
		{"10.0.0.2:90", "192.0.2.20, 10.0.0.3", 204},
		{"10.0.0.2:90", "garbage", 403},
		{"198.51.100.1:90", "", 403},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		r.Header.Set("X-Forwarded-For", c.xff)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s via %s: %d want %d", c.xff, c.peer, w.Code, c.want)
		}
	}
	f.prefixes = nil // database not available -> fail closed
	r := httptest.NewRequest("GET", "/client/livecal.exe", nil)
	r.RemoteAddr = "192.0.2.20:90"
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 403 {
		t.Fatal("failed open without ASN data")
	}
}
