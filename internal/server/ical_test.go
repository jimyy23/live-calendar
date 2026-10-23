package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestICSLiveWorkingBlockGrowsWithStableUID(t *testing.T) {
	s, h := testServer(t)
	u, err := s.store.CreateUser("icsuser")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	ping(t, s.store, u, true, now)
	feed := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/ical/"+u.UUID+".ics", nil))
		if w.Code != 200 {
			t.Fatalf("ICS status %d", w.Code)
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("feed may be cached")
		}
		return w.Body.String()
	}
	first := feed()
	for _, want := range []string{"SUMMARY:Working", "SEQUENCE:1", "LAST-MODIFIED:", "DTEND:" + time.Unix(now+lookahead, 0).UTC().Format("20060102T150405Z")} {
		if !strings.Contains(first, want) {
			t.Fatalf("first feed missing %q: %s", want, first)
		}
	}
	evs := working(t, s.store, u)
	if len(evs) != 1 || !evs[0].Auto {
		t.Fatal("expected live auto block", evs)
	}
	uid := fmt.Sprintf("UID:%d-%s@", evs[0].ID, u.UUID[:12])
	ping(t, s.store, u, true, now+60)
	second := feed()
	for _, want := range []string{uid, "SEQUENCE:2", "DTEND:" + time.Unix(now+60+lookahead, 0).UTC().Format("20060102T150405Z")} {
		if !strings.Contains(second, want) {
			t.Fatalf("updated feed missing %q: %s", want, second)
		}
	}
	if strings.Count(second, "BEGIN:VEVENT") != 1 {
		t.Fatal("live block duplicated")
	}
}
