package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed web
var webFS embed.FS

type Server struct {
	cfg    *Config
	store  *Store
	secret []byte
	tpl    *template.Template
	limit  *rateLimiter
}

func New(cfg *Config) (*Server, error) {
	st, err := OpenStore(cfg.dbPath())
	if err != nil {
		return nil, err
	}
	st.Sync = SyncOptions{FillOnline: cfg.FillOnline, EatOffline: cfg.EatOffline}
	sec, err := st.Secret()
	if err != nil {
		return nil, err
	}
	tpl, err := template.ParseFS(webFS, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	log.Printf("sync: fill_online=%v eat_offline=%v, timezone=%s", cfg.FillOnline, cfg.EatOffline, cfg.TZName)
	return &Server{cfg: cfg, store: st, secret: []byte(sec), tpl: tpl, limit: newRateLimiter()}, nil
}

func (s *Server) Close() error { return s.store.Close() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("POST /admin/users", s.admin(s.createUser))
	mux.HandleFunc("POST /admin/users/{id}/rotate", s.admin(s.rotateUser))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.admin(s.deleteUser))

	mux.HandleFunc("GET /download/livecal.exe", s.admin(s.downloadExe))
	mux.HandleFunc("GET /download/install.cmd", s.admin(s.downloadInstall))
	mux.HandleFunc("GET /download/uninstall.cmd", s.admin(s.downloadUninstall))
	// the installer itself fetches the exe without a session; the binary is not secret
	mux.HandleFunc("GET /client/livecal.exe", s.downloadExe)

	mux.HandleFunc("GET /view/{uuid}", s.viewPage)
	mux.HandleFunc("GET /edit/{uuid}", s.editPage)
	mux.HandleFunc("POST /edit/{uuid}", s.editLogin)
	mux.HandleFunc("GET /ical/{file}", s.ical)

	mux.HandleFunc("GET /api/cal/{uuid}/events", s.calAccess(false, s.apiEvents))
	mux.HandleFunc("GET /api/cal/{uuid}/status", s.calAccess(false, s.apiStatus))
	mux.HandleFunc("POST /api/cal/{uuid}/message", s.calAccess(false, s.apiMessage))
	mux.HandleFunc("POST /api/cal/{uuid}/events", s.calAccess(true, s.apiCreateEvent))
	mux.HandleFunc("PUT /api/cal/{uuid}/events/{id}", s.calAccess(true, s.apiUpdateEvent))
	mux.HandleFunc("DELETE /api/cal/{uuid}/events/{id}", s.calAccess(true, s.apiDeleteEvent))

	mux.HandleFunc("GET /api/client/hello", s.clientAuth(s.clientHello))
	mux.HandleFunc("POST /api/client/ping", s.clientAuth(s.clientPing))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	go func() {
		for {
			s.store.Cleanup(time.Now().Unix())
			time.Sleep(6 * time.Hour)
		}
	}()
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

// ---- cookies ----

func (s *Server) sign(v string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(v))
	return v + "." + hex.EncodeToString(m.Sum(nil))
}

func (s *Server) verify(signed string) (string, bool) {
	i := strings.LastIndexByte(signed, '.')
	if i < 0 {
		return "", false
	}
	v := signed[:i]
	return v, hmac.Equal([]byte(s.sign(v)), []byte(signed))
}

func (s *Server) secure() bool { return strings.HasPrefix(s.cfg.BaseURL, "https://") }

func (s *Server) setCookie(w http.ResponseWriter, name, val string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
}

// admin session: "admin|<expiry>|<password hash prefix>" so changing ADMIN_PASSWORD logs everyone out
func (s *Server) pwTag() string {
	h := sha256.Sum256([]byte(s.cfg.AdminPassword))
	return hex.EncodeToString(h[:6])
}

func (s *Server) isAdmin(r *http.Request) bool {
	c, err := r.Cookie("lc_admin")
	if err != nil {
		return false
	}
	v, ok := s.verify(c.Value)
	if !ok {
		return false
	}
	p := strings.Split(v, "|")
	if len(p) != 3 || p[0] != "admin" || p[2] != s.pwTag() {
		return false
	}
	exp, _ := strconv.ParseInt(p[1], 10, 64)
	return time.Now().Unix() < exp
}

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		o = r.Header.Get("Referer")
	}
	if o == "" {
		return true
	}
	o = strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
	host, _, _ := strings.Cut(o, "/")
	return host == r.Host
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		ip, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(ip)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// ---- rate limiter (fixed window per key) ----

type rateLimiter struct {
	mu sync.Mutex
	m  map[string][2]int64 // window start, count
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string][2]int64{}} }

func (l *rateLimiter) allow(key string, max int64, window int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	if len(l.m) > 10000 {
		l.m = map[string][2]int64{}
	}
	v := l.m[key]
	if now-v[0] >= window {
		v = [2]int64{now, 0}
	}
	v[1]++
	l.m[key] = v
	return v[1] <= max
}

// ---- pages ----

type userRow struct {
	*User
	Status  LiveStatus
	ViewURL string
	EditURL string
	ICSURL  string
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Println("template:", err)
	}
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		s.render(w, "login.html", map[string]any{"Error": r.URL.Query().Get("e") != ""})
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now().Unix()
	var rows []userRow
	for _, u := range users {
		st, _ := s.store.Status(u.ID, now)
		rows = append(rows, userRow{User: u, Status: st,
			ViewURL: s.cfg.BaseURL + "/view/" + u.UUID + "?team_token=" + s.cfg.TeamToken,
			EditURL: s.cfg.BaseURL + "/edit/" + u.UUID + "?token=" + u.Token,
			ICSURL:  s.cfg.BaseURL + "/ical/" + u.UUID + ".ics"})
	}
	_, exeErr := os.Stat(s.cfg.ClientExe)
	s.render(w, "admin.html", map[string]any{
		"Users": rows, "BaseURL": s.cfg.BaseURL, "Flash": r.URL.Query().Get("msg"),
		"ExeOK": exeErr == nil, "FillOnline": s.cfg.FillOnline, "EatOffline": s.cfg.EatOffline,
		"TZ": s.cfg.TZName,
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow("login:"+clientIP(r), 10, 300) {
		http.Error(w, "too many attempts, wait a few minutes", http.StatusTooManyRequests)
		return
	}
	pw := r.FormValue("password")
	if subtle.ConstantTimeCompare([]byte(pw), []byte(s.cfg.AdminPassword)) != 1 {
		http.Redirect(w, r, "/?e=1", http.StatusSeeOther)
		return
	}
	exp := time.Now().Add(30 * 24 * time.Hour).Unix()
	s.setCookie(w, "lc_admin", s.sign("admin|"+strconv.FormatInt(exp, 10)+"|"+s.pwTag()), 30*86400)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, "lc_admin", "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{2,32}$`)

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("username"))
	if !usernameRe.MatchString(name) {
		http.Redirect(w, r, "/?msg=Username+must+be+2-32+chars:+letters,+digits,+.+_+-", http.StatusSeeOther)
		return
	}
	if _, err := s.store.CreateUser(name); err != nil {
		http.Redirect(w, r, "/?msg=Username+already+exists", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?msg=User+created", http.StatusSeeOther)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) rotateUser(w http.ResponseWriter, r *http.Request) {
	s.store.RotateToken(pathID(r))
	http.Redirect(w, r, "/?msg=Token+rotated+(re-install+the+client+with+the+new+token)", http.StatusSeeOther)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	s.store.DeleteUser(pathID(r))
	http.Redirect(w, r, "/?msg=User+deleted", http.StatusSeeOther)
}

// ---- calendar pages ----

type calPage struct {
	User    *User
	Mode    string // view | edit
	TZ      string
	ICSURL  string
	ViewURL string
}

func (s *Server) teamOK(r *http.Request) bool {
	t := r.URL.Query().Get("team_token")
	if t == "" {
		if c, err := r.Cookie("lc_team"); err == nil {
			if v, ok := s.verify(c.Value); ok {
				t = v
			}
		}
	}
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.TeamToken)) == 1
}

func (s *Server) viewPage(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByUUID(r.PathValue("uuid"))
	if err != nil || !(s.teamOK(r) || s.isAdmin(r)) {
		http.Error(w, "not found or invalid team token", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("team_token") != "" {
		// remember the team token so API calls from the page work without putting it in every request
		s.setCookie(w, "lc_team", s.sign(s.cfg.TeamToken), 30*86400)
	}
	s.render(w, "calendar.html", calPage{User: u, Mode: "view", TZ: s.cfg.TZName,
		ICSURL: s.cfg.BaseURL + "/ical/" + u.UUID + ".ics"})
}

func editCookieName(u *User) string { return "lc_edit_" + u.UUID[:12] }

func (s *Server) canEdit(r *http.Request, u *User) bool {
	if s.isAdmin(r) {
		return true
	}
	c, err := r.Cookie(editCookieName(u))
	if err != nil {
		return false
	}
	v, ok := s.verify(c.Value)
	return ok && subtle.ConstantTimeCompare([]byte(v), []byte(u.UUID+"|"+u.Token)) == 1
}

func (s *Server) editPage(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByUUID(r.PathValue("uuid"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(u.Token)) == 1 {
			s.setCookie(w, editCookieName(u), s.sign(u.UUID+"|"+u.Token), 30*86400)
			http.Redirect(w, r, "/edit/"+u.UUID, http.StatusSeeOther)
			return
		}
		s.render(w, "token.html", map[string]any{"UUID": u.UUID, "Error": true})
		return
	}
	if !s.canEdit(r, u) {
		s.render(w, "token.html", map[string]any{"UUID": u.UUID})
		return
	}
	s.render(w, "calendar.html", calPage{User: u, Mode: "edit", TZ: s.cfg.TZName,
		ICSURL: s.cfg.BaseURL + "/ical/" + u.UUID + ".ics"})
}

func (s *Server) editLogin(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByUUID(r.PathValue("uuid"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !s.limit.allow("edit:"+clientIP(r), 10, 300) {
		http.Error(w, "too many attempts, wait a few minutes", http.StatusTooManyRequests)
		return
	}
	tok := strings.TrimSpace(r.FormValue("token"))
	if subtle.ConstantTimeCompare([]byte(tok), []byte(u.Token)) != 1 {
		s.render(w, "token.html", map[string]any{"UUID": u.UUID, "Error": true})
		return
	}
	s.setCookie(w, editCookieName(u), s.sign(u.UUID+"|"+u.Token), 30*86400)
	http.Redirect(w, r, "/edit/"+u.UUID, http.StatusSeeOther)
}

// ---- calendar JSON API ----

type calHandler func(w http.ResponseWriter, r *http.Request, u *User)

func (s *Server) calAccess(write bool, h calHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.store.UserByUUID(r.PathValue("uuid"))
		if err != nil {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		ok := s.canEdit(r, u)
		if !write && !ok {
			ok = s.teamOK(r)
		}
		if !ok {
			jsonErr(w, http.StatusForbidden, "forbidden")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r) {
			jsonErr(w, http.StatusForbidden, "bad origin")
			return
		}
		h(w, r, u)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request, u *User) {
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if to <= from || to-from > 62*86400 {
		jsonErr(w, 400, "bad range")
		return
	}
	evs, err := s.store.Events(u.ID, from, to)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	st, _ := s.store.Status(u.ID, time.Now().Unix())
	writeJSON(w, map[string]any{"events": evs, "status": st})
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request, u *User) {
	st, err := s.store.Status(u.ID, time.Now().Unix())
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, st)
}

func decodeEvent(w http.ResponseWriter, r *http.Request) (*Event, error) {
	var e Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&e); err != nil {
		return nil, errors.New("bad json")
	}
	if e.Status != StatusWorking && e.Status != StatusUnavailable {
		return nil, errors.New("status must be working or unavailable")
	}
	if e.End <= e.Start || e.End-e.Start > 14*86400 {
		return nil, errors.New("bad time range")
	}
	e.Description = cleanText(e.Description, 500, true)
	return &e, nil
}

func (s *Server) apiCreateEvent(w http.ResponseWriter, r *http.Request, u *User) {
	e, err := decodeEvent(w, r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if err := s.store.CreateEvent(u.ID, e); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, e)
}

func (s *Server) apiUpdateEvent(w http.ResponseWriter, r *http.Request, u *User) {
	e, err := decodeEvent(w, r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	e.ID = pathID(r)
	if err := s.store.UpdateEvent(u.ID, e); err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	writeJSON(w, e)
}

func (s *Server) apiDeleteEvent(w http.ResponseWriter, r *http.Request, u *User) {
	if err := s.store.DeleteEvent(u.ID, pathID(r)); err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// cleanText strips control / format characters (incl. bidi overrides), trims and truncates.
func cleanText(s string, max int, keepNewlines bool) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if r == '\n' && keepNewlines {
			// fine
		} else if r == '\t' {
			r = ' '
		} else if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) apiMessage(w http.ResponseWriter, r *http.Request, u *User) {
	if !s.limit.allow("msg:"+clientIP(r), 5, 600) || !s.limit.allow("msgu:"+u.UUID, 30, 600) {
		jsonErr(w, http.StatusTooManyRequests, "Too many messages, please wait a bit.")
		return
	}
	var in struct{ From, Text string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		jsonErr(w, 400, "bad json")
		return
	}
	text := cleanText(in.Text, 300, true)
	from := cleanText(in.From, 40, false)
	if text == "" {
		jsonErr(w, 400, "Message is empty")
		return
	}
	if err := s.store.AddMessage(u.ID, from, text, time.Now().Unix()); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	st, _ := s.store.Status(u.ID, time.Now().Unix())
	writeJSON(w, map[string]any{"ok": true, "delivering": st.Running})
}

// ---- Windows client API ----

type clientHandler func(w http.ResponseWriter, r *http.Request, u *User)

func (s *Server) clientAuth(h clientHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, tok, ok := r.BasicAuth()
		if ok && !s.limit.allow("client:"+clientIP(r)+":"+name, 120, 60) {
			jsonErr(w, http.StatusTooManyRequests, "slow down")
			return
		}
		var u *User
		if ok {
			u, _ = s.store.UserByName(name)
		}
		if u == nil || subtle.ConstantTimeCompare([]byte(tok), []byte(u.Token)) != 1 {
			jsonErr(w, http.StatusUnauthorized, "invalid username or token")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) clientHello(w http.ResponseWriter, r *http.Request, u *User) {
	writeJSON(w, map[string]any{"ok": true, "username": u.Username})
}

var deviceRe = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

func (s *Server) clientPing(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Device string `json:"device"`
		Active bool   `json:"active"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil || !deviceRe.MatchString(in.Device) {
		jsonErr(w, 400, "bad ping")
		return
	}
	msgs, err := s.store.Ping(u.ID, in.Device, in.Active, time.Now().Unix())
	if err != nil {
		log.Println("ping:", err)
		jsonErr(w, 500, "error")
		return
	}
	if msgs == nil {
		msgs = []Message{}
	}
	writeJSON(w, map[string]any{"ok": true, "messages": msgs, "interval": 60})
}
