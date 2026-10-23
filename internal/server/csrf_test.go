package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ADMIN_PASSWORD", "pw")
	t.Setenv("TEAM_PING_TOKEN", "team")
	t.Setenv("DATA_DIR", dir)
	t.Setenv("BASE_URL", "https://cal.example.com/")
	t.Setenv("CLIENT_EXE", filepath.Join(dir, "none.exe"))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, s.Handler()
}

func do(h http.Handler, method, path string, form url.Values, hdr map[string]string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Reproduces prod: proxy rewrites Host, browser sends Origin: null.
func TestAdminCreateUserBehindProxy(t *testing.T) {
	s, h := testServer(t)
	login := do(h, "POST", "/login", url.Values{"password": {"pw"}}, nil, nil)
	cookies := login.Result().Cookies()
	page := do(h, "GET", "/", nil, nil, cookies).Body.String()
	m := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no csrf token on admin page")
	}
	weird := map[string]string{"Origin": "null", "Host": "livecal:8933"}

	w := do(h, "POST", "/admin/users", url.Values{"username": {"alice"}, "csrf": {m[1]}}, weird, cookies)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "User+created") {
		t.Fatalf("create failed: %d %s %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if _, err := s.store.UserByName("alice"); err != nil {
		t.Fatal("user not created")
	}

	// forged request (cross-site form) without the token is refused
	w = do(h, "POST", "/admin/users", url.Values{"username": {"mallory"}}, map[string]string{"Origin": "https://evil.test"}, cookies)
	if _, err := s.store.UserByName("mallory"); err == nil || strings.Contains(w.Header().Get("Location"), "User+created") {
		t.Fatal("forged request accepted")
	}
}

func TestAPIRequiresJSON(t *testing.T) {
	s, h := testServer(t)
	u, _ := s.store.CreateUser("bob")
	cookie := &http.Cookie{Name: editCookieName(u), Value: s.sign(u.UUID + "|" + u.Token)}
	// cross-site HTML form post (text/plain trick) is rejected
	r := httptest.NewRequest("POST", "/api/cal/"+u.UUID+"/events", strings.NewReader(`{"status":"working","start":1,"end":2}`))
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("got %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/cal/"+u.UUID+"/events", strings.NewReader(`{"status":"working","start":1000,"end":2000}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "null")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("json create failed: %d %s", w.Code, w.Body)
	}
}
