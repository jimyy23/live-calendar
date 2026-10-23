package server

import (
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

const asnSource = "https://raw.githubusercontent.com/ipverse/as-ip-blocks/master/as/"

type asnFilter struct {
	mu          sync.RWMutex
	allowed     map[string]bool
	prefixes    []netip.Prefix
	proxies     []netip.Prefix
	cache       string
	lastDenyLog time.Time
}

func newASNFilter(c *Config) (*asnFilter, error) {
	if strings.TrimSpace(c.AllowedASNs) == "" {
		return nil, nil
	}
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return nil, err
	}
	f := &asnFilter{allowed: map[string]bool{}, cache: c.DataDir}
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
	if err := f.refresh(); err != nil {
		log.Printf("ASN ipverse load failed (unmatched traffic denied): %v", err)
	}
	log.Printf("ASN gate enabled: allowed=%q trusted_proxies=%q loaded_ranges=%d", c.AllowedASNs, c.TrustedProxies, len(f.prefixes))
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

func parseIPverse(b []byte, asn string) ([]netip.Prefix, error) {
	var data struct {
		ASN      uint32 `json:"asn"`
		Prefixes struct {
			IPv4 []string `json:"ipv4"`
			IPv6 []string `json:"ipv6"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	if strconv.FormatUint(uint64(data.ASN), 10) != asn {
		return nil, fmt.Errorf("ipverse ASN mismatch")
	}
	var prefixes []netip.Prefix
	for _, v := range append(data.Prefixes.IPv4, data.Prefixes.IPv6...) {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return nil, err
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("unexpected default route")
		}
		prefixes = append(prefixes, p.Masked())
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("AS%s has no ipverse prefixes", asn)
	}
	return prefixes, nil
}

func (f *asnFilter) asnPrefixes(asn string) ([]netip.Prefix, error) {
	path := filepath.Join(f.cache, "asn-ipverse-"+asn+".json")
	b, _ := os.ReadFile(path)
	cached, cacheErr := parseIPverse(b, asn)
	if st, err := os.Stat(path); err == nil && cacheErr == nil && time.Since(st.ModTime()) < 24*time.Hour {
		return cached, nil
	}
	client := &http.Client{Timeout: 30 * time.Second}
	r, err := client.Get(asnSource + asn + "/aggregated.json")
	if err == nil {
		defer r.Body.Close()
		if r.StatusCode != 200 {
			err = fmt.Errorf("ipverse HTTP %d", r.StatusCode)
		} else {
			b, err = io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
			if len(b) > 8<<20 {
				err = fmt.Errorf("ipverse file too large")
			}
		}
	}
	var prefixes []netip.Prefix
	if err == nil {
		prefixes, err = parseIPverse(b, asn)
	}
	if err != nil {
		if cacheErr == nil {
			log.Printf("ASN AS%s ipverse refresh failed; using cached ranges: %v", asn, err)
			return cached, nil
		}
		return nil, fmt.Errorf("AS%s ipverse: %w", asn, err)
	}
	if err = os.WriteFile(path+".tmp", b, 0600); err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err != nil {
		log.Printf("ASN AS%s cache write failed: %v", asn, err)
	}
	log.Printf("ASN AS%s: loaded %d ranges from ipverse", asn, len(prefixes))
	return prefixes, nil
}

func (f *asnFilter) refresh() error {
	var prefixes []netip.Prefix
	for asn := range f.allowed {
		ps, err := f.asnPrefixes(asn)
		if err != nil {
			return err
		}
		prefixes = append(prefixes, ps...)
	}
	f.mu.Lock()
	f.prefixes = prefixes
	f.mu.Unlock()
	return nil
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
			// Bound diagnostic volume and log no URL, cookies, credentials or body.
			s.asn.mu.Lock()
			if time.Since(s.asn.lastDenyLog) >= 10*time.Second {
				s.asn.lastDenyLog = time.Now()
				reason := "client IP not in allowed ASN ranges"
				if len(s.asn.prefixes) == 0 {
					reason = "no ASN ranges loaded; check database download logs"
				} else if !ip.IsValid() {
					reason = "invalid or missing forwarded IP"
				} else if !containsPrefix(s.asn.proxies, peer.Unmap()) {
					reason = "connection peer is not a trusted proxy (or direct client is not allowed)"
				}
				xff := r.Header.Get("X-Forwarded-For")
				if len(xff) > 512 {
					xff = xff[:512]
				}
				log.Printf("ASN denied: peer=%q forwarded_for=%q evaluated_ip=%q ranges=%d reason=%q", r.RemoteAddr, xff, ip.String(), len(s.asn.prefixes), reason)
			}
			s.asn.mu.Unlock()
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
