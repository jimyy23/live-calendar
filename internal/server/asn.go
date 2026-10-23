package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const asnSource = "https://geoip.oxl.app/file/asn_small.json.zip"

type asnFilter struct {
	mu       sync.RWMutex
	allowed  map[string]bool
	prefixes []netip.Prefix
	proxies  []netip.Prefix
	cache    string
}

func newASNFilter(c *Config) (*asnFilter, error) {
	if strings.TrimSpace(c.AllowedASNs) == "" {
		return nil, nil
	}
	f := &asnFilter{allowed: map[string]bool{}, cache: filepath.Join(c.DataDir, "asn-small.json.zip")}
	for _, v := range strings.Split(c.AllowedASNs, ",") {
		v = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "AS")
		n, e := strconv.ParseUint(v, 10, 32)
		if e != nil || n == 0 {
			return nil, fmt.Errorf("invalid ALLOWED_ASNS entry %q", v)
		}
		f.allowed[strconv.FormatUint(n, 10)] = true
	}
	for _, v := range strings.Split(c.TrustedProxies, ",") {
		if strings.TrimSpace(v) == "" {
			continue
		}
		p, e := netip.ParsePrefix(strings.TrimSpace(v))
		if e != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: %w", e)
		}
		f.proxies = append(f.proxies, p)
	}
	if b, e := os.ReadFile(f.cache); e == nil {
		if e = f.load(b); e != nil {
			log.Printf("ASN cached data rejected: %v", e)
		}
	}
	st, e := os.Stat(f.cache)
	if len(f.prefixes) == 0 || e != nil || time.Since(st.ModTime()) > 24*time.Hour {
		if e = f.refresh(); e != nil {
			log.Printf("ASN download failed (unmatched traffic denied): %v", e)
		}
	}
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			if e := f.refresh(); e != nil {
				log.Printf("ASN refresh failed; retaining last good data: %v", e)
			}
		}
	}()
	return f, nil
}

func (f *asnFilter) load(b []byte) error {
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return e
	}
	var data map[string]struct {
		IPv4 []string `json:"ipv4"`
		IPv6 []string `json:"ipv6"`
	}
	found := false
	for _, file := range z.File {
		if !strings.HasSuffix(file.Name, ".json") {
			continue
		}
		r, e := file.Open()
		if e != nil {
			return e
		}
		e = json.NewDecoder(io.LimitReader(r, 256<<20)).Decode(&data)
		r.Close()
		if e != nil {
			return e
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf("ASN archive missing JSON")
	}
	var prefixes []netip.Prefix
	for asn := range f.allowed {
		entry, ok := data[asn]
		if !ok {
			return fmt.Errorf("AS%s missing from dataset", asn)
		}
		for _, v := range append(entry.IPv4, entry.IPv6...) {
			p, e := netip.ParsePrefix(v)
			if e != nil {
				return e
			}
			prefixes = append(prefixes, p)
		}
	}
	if len(prefixes) == 0 {
		return fmt.Errorf("ASN allowlist has no ranges")
	}
	f.mu.Lock()
	f.prefixes = prefixes
	f.mu.Unlock()
	return nil
}
func (f *asnFilter) refresh() error {
	client := &http.Client{Timeout: 90 * time.Second}
	r, e := client.Get(asnSource)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("ASN download HTTP %d", r.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 64<<20+1))
	if e != nil {
		return e
	}
	if len(b) > 64<<20 {
		return fmt.Errorf("ASN archive too large")
	}
	if e = f.load(b); e != nil {
		return e
	}
	if e = os.WriteFile(f.cache+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(f.cache+".tmp", f.cache)
}
func containsPrefix(ps []netip.Prefix, ip netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Trust X-Forwarded-For only from explicitly configured proxy networks, then
// walk right-to-left so a visitor cannot spoof the leftmost address.
func (f *asnFilter) requestIP(r *http.Request) netip.Addr {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	ip = ip.Unmap()
	if !containsPrefix(f.proxies, ip) {
		return ip
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		if !containsPrefix(f.proxies, ip) {
			break
		}
		next, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if e != nil {
			return netip.Addr{}
		}
		ip = next.Unmap()
	}
	return ip
}
func (s *Server) asnGate(next http.Handler) http.Handler {
	if s.asn == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local Docker healthcheck only. All external paths, including healthz,
		// client pings and ICS, remain gated.
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		peer, _ := netip.ParseAddr(host)
		if r.URL.Path == "/healthz" && peer.IsLoopback() && r.Header.Get("X-Forwarded-For") == "" {
			next.ServeHTTP(w, r)
			return
		}
		ip := s.asn.requestIP(r)
		s.asn.mu.RLock()
		ok := containsPrefix(s.asn.prefixes, ip)
		s.asn.mu.RUnlock()
		if !ok {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
