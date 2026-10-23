package server

import (
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	s := &Server{cfg: &Config{BaseURL: "https://cal.example.com"}}
	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    bool
	}{
		{"browser form, Origin null (no-referrer)", "cal.example.com", map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}, true},
		{"browser same-origin", "cal.example.com", map[string]string{"Origin": "https://cal.example.com", "Sec-Fetch-Site": "same-origin"}, true},
		{"proxy rewrites Host", "livecal:8933", map[string]string{"Origin": "https://cal.example.com"}, true},
		{"cross-site browser", "cal.example.com", map[string]string{"Origin": "https://evil.test", "Sec-Fetch-Site": "cross-site"}, false},
		{"cross-site old browser", "cal.example.com", map[string]string{"Origin": "https://evil.test"}, false},
		{"Origin null without fetch metadata", "cal.example.com", map[string]string{"Origin": "null"}, true},
		{"no headers (curl)", "cal.example.com", nil, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/admin/users", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if got := s.sameOrigin(r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
