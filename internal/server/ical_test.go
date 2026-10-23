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
	unfolded := strings.ReplaceAll(first, "\r\n ", "")
	link := "Live Status View (Team view): " + s.cfg.BaseURL + "/view/" + u.UUID + "?team_token=" + s.cfg.TeamToken
	if !strings.Contains(unfolded, "DESCRIPTION:"+link) {
		t.Fatal("auto block without a description must include the team view link")
	}
	slotStart := now - now%liveSlot
	for _, want := range []string{"SUMMARY:Working", "SEQUENCE:1", "LAST-MODIFIED:", "DTEND:" + time.Unix(slotStart+liveSlot, 0).UTC().Format("20060102T150405Z")} {
		if !strings.Contains(first, want) {
			t.Fatalf("first feed missing %q: %s", want, first)
		}
	}
	evs := working(t, s.store, u)
	if len(evs) != 1 || !evs[0].Auto {
		t.Fatal("expected live auto block", evs)
	}
	uid := fmt.Sprintf("UID:%d-%s@", evs[0].ID, u.UUID[:12])
	ping(t, s.store, u, true, slotStart+liveSlot+60)
	second := feed()
	for _, want := range []string{uid, "SEQUENCE:2", "DTEND:" + time.Unix(slotStart+2*liveSlot, 0).UTC().Format("20060102T150405Z")} {
		if !strings.Contains(second, want) {
			t.Fatalf("updated feed missing %q: %s", want, second)
		}
	}
	if strings.Count(second, "BEGIN:VEVENT") != 1 {
		t.Fatal("live block duplicated")
	}
	manual := &Event{Start: now + 3600, End: now + 7200, Status: StatusUnavailable, Description: "Dentist\nBack soon"}
	if err := s.store.CreateEvent(u.ID, manual); err != nil {
		t.Fatal(err)
	}
	withManual := strings.ReplaceAll(feed(), "\r\n ", "")
	if !strings.Contains(withManual, `DESCRIPTION:Dentist\nBack soon\n\n`+link) {
		t.Fatal("manual block must preserve description and append team view link")
	}
}
