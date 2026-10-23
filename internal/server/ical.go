package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func icsEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`)
	return r.Replace(s)
}

// icsFold folds lines longer than 75 octets per RFC 5545.
func icsFold(line string) string {
	if len(line) <= 75 {
		return line + "\r\n"
	}
	var b strings.Builder
	cur := 0
	for _, r := range line {
		l := len(string(r))
		if cur+l > 75 {
			b.WriteString("\r\n ")
			cur = 1
		}
		b.WriteRune(r)
		cur += l
	}
	b.WriteString("\r\n")
	return b.String()
}

func (s *Server) ical(w http.ResponseWriter, r *http.Request) {
	uuid, ok := strings.CutSuffix(r.PathValue("file"), ".ics")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, err := s.store.UserByUUID(uuid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	evs, err := s.store.Events(u.ID, now.AddDate(0, -3, 0).Unix(), now.AddDate(0, 6, 0).Unix())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(s.cfg.BaseURL, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	stamp := now.UTC().Format("20060102T150405Z")

	var b strings.Builder
	line := func(f string, a ...any) { b.WriteString(icsFold(fmt.Sprintf(f, a...))) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Live Calendar//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:%s", icsEscape(u.Username+" - Live Calendar"))
	line("X-WR-TIMEZONE:%s", s.cfg.TZName)
	line("REFRESH-INTERVAL;VALUE=DURATION:PT15M")
	line("X-PUBLISHED-TTL:PT15M")
	for _, e := range evs {
		title := "Working"
		busy := "BUSY"
		if e.Status == StatusUnavailable {
			title = "Unavailable"
		}
		if e.Description != "" {
			title += ": " + strings.SplitN(e.Description, "\n", 2)[0]
		}
		line("BEGIN:VEVENT")
		line("UID:%d-%s@%s", e.ID, u.UUID[:12], host)
		line("DTSTAMP:%s", stamp)
		line("DTSTART:%s", time.Unix(e.Start, 0).UTC().Format("20060102T150405Z"))
		line("DTEND:%s", time.Unix(e.End, 0).UTC().Format("20060102T150405Z"))
		line("SUMMARY:%s", icsEscape(title))
		if e.Description != "" {
			line("DESCRIPTION:%s", icsEscape(e.Description))
		}
		line("TRANSP:OPAQUE")
		line("X-MICROSOFT-CDO-BUSYSTATUS:%s", busy)
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(b.String()))
}
