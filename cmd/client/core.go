package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	idleThreshold = 5 * time.Minute
	pingInterval  = 60 * time.Second
	checkInterval = 5 * time.Second
	deadAfter     = 14 * 24 * time.Hour
	minFailsInRun = 30 // consecutive failed pings (with network up) required in this run before self-uninstall
	clientUA      = "livecal-client/1"
)

type Config struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Token    string `json:"token"`
	Device   string `json:"device"`
}

// State is persisted so the 14-day timer survives reboots.
type State struct {
	LastOK int64 `json:"last_ok"`
}

type Message struct {
	From string `json:"from"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

var ErrAuth = errors.New("invalid username or token")

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) *Client {
	cfg.Server = strings.TrimRight(cfg.Server, "/")
	return &Client{cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(method, path string, body any, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.cfg.Server+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Token)
	req.Header.Set("User-Agent", clientUA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Hello() error { return c.do("GET", "/api/client/hello", nil, nil) }

// Ping sends the only data this client ever reports: a random device id and whether
// someone used the keyboard/mouse in the last 5 minutes.
func (c *Client) Ping(active bool) ([]Message, error) {
	var out struct {
		OK       bool      `json:"ok"`
		Messages []Message `json:"messages"`
	}
	err := c.do("POST", "/api/client/ping", map[string]any{"device": c.cfg.Device, "active": active}, &out)
	if err == nil && !out.OK {
		err = errors.New("bad response")
	}
	return out.Messages, err
}

// networkUp reports whether this PC has a usable non-loopback network interface,
// determined locally without contacting any host.
func networkUp() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if ok && !ipn.IP.IsLinkLocalUnicast() && !ipn.IP.IsLoopback() {
				return true
			}
		}
	}
	return false
}

// Deadman decides when the server should be considered gone for good.
type Deadman struct {
	statePath string
	st        State
	failsRun  int
}

func NewDeadman(dir string) *Deadman {
	d := &Deadman{statePath: filepath.Join(dir, "state.json")}
	if readJSON(d.statePath, &d.st) != nil || d.st.LastOK == 0 {
		d.st.LastOK = time.Now().Unix()
		writeJSON(d.statePath, d.st)
	}
	return d
}

// OK records a successful ping. The timestamp is persisted at most hourly to
// avoid a disk write every minute; being up to an hour stale is irrelevant for a 14-day rule.
func (d *Deadman) OK(now time.Time) {
	d.failsRun = 0
	if now.Unix()-d.st.LastOK >= 3600 {
		d.st.LastOK = now.Unix()
		writeJSON(d.statePath, d.st)
	}
}

// Fail records a failed ping; returns true when the client should uninstall itself.
func (d *Deadman) Fail(now time.Time, netUp bool) bool {
	if !netUp {
		return false
	}
	d.failsRun++
	return d.failsRun >= minFailsInRun && now.Sub(time.Unix(d.st.LastOK, 0)) >= deadAfter
}

// Runner holds the main loop logic, independent of the Windows APIs.
type Runner struct {
	Client    *Client
	Dead      *Deadman
	IdleFor   func() time.Duration
	NetUp     func() bool
	Popup     func(Message)
	Uninstall func()
	Now       func() time.Time
	Sleep     func(time.Duration)

	lastPing   time.Time
	lastActive bool
	fails      int
}

// Step runs one check; returns false when the loop should stop.
func (r *Runner) Step() bool {
	now := r.Now()
	active := r.IdleFor() < idleThreshold
	interval := pingInterval
	if r.fails > 3 {
		interval = 5 * time.Minute // back off while the server is unreachable
	}
	due := r.lastPing.IsZero() || now.Sub(r.lastPing) >= interval || (active != r.lastActive && r.fails == 0)
	if !due {
		return true
	}
	r.lastPing, r.lastActive = now, active
	msgs, err := r.Client.Ping(active)
	if err != nil {
		r.fails++
		if r.Dead.Fail(now, r.NetUp()) {
			r.Uninstall()
			return false
		}
		return true
	}
	r.fails = 0
	r.Dead.OK(now)
	for _, m := range msgs {
		r.Popup(m)
	}
	return true
}

func (r *Runner) Run() {
	for r.Step() {
		r.Sleep(checkInterval)
	}
}
