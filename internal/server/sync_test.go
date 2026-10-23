package server

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (*Store, *User) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	u, err := s.CreateUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	return s, u
}

func working(t *testing.T, s *Store, u *User) []Event {
	t.Helper()
	evs, err := s.Events(u.ID, 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Status == StatusWorking {
			out = append(out, e)
		}
	}
	return out
}

func ping(t *testing.T, s *Store, u *User, active bool, now int64) {
	t.Helper()
	if _, err := s.Ping(u.ID, "pc1", active, now); err != nil {
		t.Fatal(err)
	}
}

const h = 3600

// Spec example: 2-5pm booked, client starts at 3pm, offline 3:00-3:10.
// 3:00-3:10 gets eaten, 2-3pm untouched, rest of the block kept.
func TestEatDoesNotTouchPast(t *testing.T) {
	s, u := newTestStore(t)
	base := int64(1_800_000_000)
	s.CreateEvent(u.ID, &Event{Start: base + 14*h, End: base + 17*h, Status: StatusWorking})
	for now := base + 15*h; now <= base+15*h+600; now += 60 {
		ping(t, s, u, false, now)
	}
	ping(t, s, u, true, base+15*h+660)
	w := working(t, s, u)
	if len(w) < 2 || w[0].Start != base+14*h || w[0].End != base+15*h {
		t.Fatalf("past part changed: %+v", w)
	}
	last := w[len(w)-1]
	if last.End != base+17*h {
		t.Fatalf("future part lost: %+v", w)
	}
}

func TestFillExtendsAndGapUntouched(t *testing.T) {
	s, u := newTestStore(t)
	base := int64(1_800_000_000)
	for now := base; now <= base+600; now += 60 {
		ping(t, s, u, true, now)
	}
	w := working(t, s, u)
	if len(w) != 1 || w[0].Start != base || w[0].End != base+liveSlot {
		t.Fatalf("expected one growing block: %+v", w)
	}
	// client not running for an hour: nothing in the gap is created
	ping(t, s, u, true, base+2*h)
	w = working(t, s, u)
	if len(w) != 2 || w[1].Start != base+2*h {
		t.Fatalf("gap filled or block not created: %+v", w)
	}
}

func TestLiveQuarterHoursAndShortGapMerge(t *testing.T) {
	s, u := newTestStore(t)
	base := int64(1_800_000_000)
	ping(t, s, u, true, base+60) // :01 -> :00–:15
	w := working(t, s, u)
	if len(w) != 1 || w[0].Start != base || w[0].End != base+900 {
		t.Fatal(w)
	}
	id := w[0].ID
	ping(t, s, u, false, base+120)
	w = working(t, s, u)
	if len(w) != 1 || w[0].End != base+900 {
		t.Fatal("offline fragmented active slot", w)
	}
	ping(t, s, u, true, base+16*60) // :16 -> :00–:30
	w = working(t, s, u)
	if len(w) != 1 || w[0].ID != id || w[0].End != base+1800 {
		t.Fatal(w)
	}
	ping(t, s, u, true, base+46*60) // :45 slot is within 30 min of :30 end
	w = working(t, s, u)
	if len(w) != 1 || w[0].End != base+3600 {
		t.Fatal("short gap not merged", w)
	}
	ping(t, s, u, true, base+2*h) // gap >30 min -> separate block
	w = working(t, s, u)
	if len(w) != 2 || w[1].Start != base+2*h {
		t.Fatal("long gap merged", w)
	}
}

func TestLiveNoDurationCapAndManualDescriptionPreserved(t *testing.T) {
	s, u := newTestStore(t)
	base := int64(1_800_000_000)
	for at := base; at < base+16*86400; at += 900 {
		ping(t, s, u, true, at)
	}
	w := working(t, s, u)
	if len(w) != 1 || w[0].End != base+16*86400 {
		t.Fatal("duration capped", w)
	}
	manual := &Event{Start: base + 16*86400 + 60, End: base + 16*86400 + 600, Status: StatusWorking, Description: "Keep this"}
	if err := s.CreateEvent(u.ID, manual); err != nil {
		t.Fatal(err)
	}
	ping(t, s, u, true, manual.Start+60)
	w = working(t, s, u)
	if len(w) != 2 || w[1].Description != "Keep this" || w[1].Start != manual.Start {
		t.Fatal("manual event changed", w)
	}
}

func TestSyncSwitches(t *testing.T) {
	s, u := newTestStore(t)
	base := int64(1_800_000_000)
	s.Sync = SyncOptions{FillOnline: false, EatOffline: true}
	ping(t, s, u, true, base)
	if w := working(t, s, u); len(w) != 0 {
		t.Fatalf("fill disabled but created: %+v", w)
	}
	if st, _ := s.Status(u.ID, base); !st.Online {
		t.Fatal("live status should still be online")
	}

	s.CreateEvent(u.ID, &Event{Start: base, End: base + h, Status: StatusWorking})
	s.Sync = SyncOptions{FillOnline: true, EatOffline: false}
	ping(t, s, u, false, base+60)
	if w := working(t, s, u); len(w) != 1 || w[0].Start != base || w[0].End != base+h {
		t.Fatalf("eat disabled but changed: %+v", w)
	}
}
