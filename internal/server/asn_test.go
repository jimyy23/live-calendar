package server

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestASNFilter(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("asn_small.json")
	w.Write([]byte(`{"13335":{"ipv4":["192.0.2.0/24"],"ipv6":["2001:db8::/32"]}}`))
	z.Close()
	f := &asnFilter{allowed: map[string]bool{"13335": true}, proxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	if e := f.load(b.Bytes()); e != nil {
		t.Fatal(e)
	}
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
