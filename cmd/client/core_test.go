package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingSendsOnlyDeviceAndActive(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if u != "alice" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true,"messages":[{"from":"bob","text":"hi","at":1}]}`))
	}))
	defer ts.Close()
	c := NewClient(Config{Server: ts.URL + "/", Username: "alice", Token: "tok", Device: "abc"})
	msgs, err := c.Ping(true)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hi" {
		t.Fatal(err, msgs)
	}
	if len(got) != 2 || got["device"] != "abc" || got["active"] != true {
		t.Fatalf("unexpected payload %v", got)
	}
	c.cfg.Token = "bad"
	if _, err := c.Ping(true); err != ErrAuth {
		t.Fatal("want auth error", err)
	}
}

func TestDeadmanSelfUninstall(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	d := NewDeadman(dir)
	// network down: never counts
	for i := 0; i < 100; i++ {
		if d.Fail(start.Add(30*24*time.Hour), false) {
			t.Fatal("uninstalled while offline")
		}
	}
	// 15 days later, but only a few fails in this run (e.g. PC just woke up)
	later := start.Add(15 * 24 * time.Hour)
	for i := 0; i < minFailsInRun-1; i++ {
		if d.Fail(later, true) {
			t.Fatal("uninstalled too early in run")
		}
	}
	if !d.Fail(later, true) {
		t.Fatal("expected uninstall")
	}
	// state persists; a success resets it
	d2 := NewDeadman(dir)
	d2.OK(later)
	d3 := NewDeadman(dir)
	for i := 0; i < 50; i++ {
		if d3.Fail(later.Add(time.Hour), true) {
			t.Fatal("uninstalled after recent success")
		}
	}
	if filepath.Base(d3.statePath) != "state.json" {
		t.Fatal()
	}
}

func TestRunnerPingsOnChangeAndInterval(t *testing.T) {
	var pings int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pings, 1)
		w.Write([]byte(`{"ok":true,"messages":[]}`))
	}))
	defer ts.Close()
	now := time.Now()
	idle := time.Duration(0)
	r := &Runner{Client: NewClient(Config{Server: ts.URL, Username: "a", Token: "b", Device: "d"}),
		Dead: NewDeadman(t.TempDir()), IdleFor: func() time.Duration { return idle },
		NetUp: func() bool { return true }, Popup: func(Message) {}, Uninstall: func() { t.Fatal("uninstall") },
		Now: func() time.Time { return now }}
	r.Step()
	now = now.Add(10 * time.Second)
	r.Step()
	if pings != 1 {
		t.Fatalf("pings=%d", pings)
	}
	idle = 6 * time.Minute // went idle -> immediate ping
	r.Step()
	now = now.Add(61 * time.Second)
	r.Step()
	if pings != 3 {
		t.Fatalf("pings=%d", pings)
	}
}
