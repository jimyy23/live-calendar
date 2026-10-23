package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	uuid TEXT NOT NULL UNIQUE,
	token TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	cal_version INTEGER NOT NULL DEFAULT 0,
	sync_cursor INTEGER NOT NULL DEFAULT 0,
	sync_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	start_at INTEGER NOT NULL,
	end_at INTEGER NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('working','unavailable')),
	description TEXT NOT NULL DEFAULT '',
	auto INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_user_time ON events(user_id, start_at, end_at);
CREATE TABLE IF NOT EXISTS devices (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	active INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL,
	last_active INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, device_id)
);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	sender TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	first_delivered_at INTEGER
);
CREATE TABLE IF NOT EXISTS deliveries (
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	PRIMARY KEY (message_id, device_id)
);
`

const (
	StatusWorking     = "working"
	StatusUnavailable = "unavailable"

	runningWindow = 150 // seconds: device counts as "client running" if pinged within this window
	lookahead     = 60  // seconds the sync decides into the future
	msgTTL        = 3600
	msgFanout     = 90 // seconds other PCs may still pick up a message after first delivery
)

var ErrNotFound = errors.New("not found")

// SyncOptions lets the operator disable either half of the Working-block sync.
type SyncOptions struct {
	FillOnline bool
	EatOffline bool
}

type Store struct {
	db   *sql.DB
	Sync SyncOptions
}

type User struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	UUID       string `json:"uuid"`
	Token      string `json:"-"`
	CreatedAt  int64  `json:"created_at"`
	CalVersion int64  `json:"-"`
}

type Event struct {
	ID          int64  `json:"id"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Auto        bool   `json:"auto"`
}

type LiveStatus struct {
	Online     bool  `json:"online"`
	Running    bool  `json:"running"`
	LastActive int64 `json:"last_active"`
	LastSeen   int64 `json:"last_seen"`
	Version    int64 `json:"version"`
}

type Message struct {
	ID        int64  `json:"-"`
	Sender    string `json:"from"`
	Body      string `json:"text"`
	CreatedAt int64  `json:"at"`
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db, Sync: SyncOptions{FillOnline: true, EatOffline: true}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Secret returns a persistent random value used to sign cookies.
func (s *Store) Secret() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM settings WHERE k='secret'`).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	v = randHex(32)
	_, err = s.db.Exec(`INSERT INTO settings(k,v) VALUES('secret',?)`, v)
	return v, err
}

// ---- users ----

const userCols = `id, username, uuid, token, created_at, cal_version`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.UUID, &u.Token, &u.CreatedAt, &u.CalVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(username string) (*User, error) {
	u := &User{Username: username, UUID: randHex(24), Token: randHex(16), CreatedAt: time.Now().Unix()}
	res, err := s.db.Exec(`INSERT INTO users(username, uuid, token, created_at) VALUES(?,?,?,?)`,
		u.Username, u.UUID, u.Token, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.ID, _ = res.LastInsertId()
	return u, nil
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UserByUUID(uuid string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE uuid=?`, uuid))
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) UserByName(name string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name))
}

func (s *Store) RotateToken(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET token=? WHERE id=?`, randHex(16), id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

// ---- events ----

func (s *Store) Events(userID, from, to int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, start_at, end_at, status, description, auto FROM events
		WHERE user_id=? AND start_at < ? AND end_at > ? ORDER BY start_at, id`, userID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Start, &e.End, &e.Status, &e.Description, &e.Auto); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func bump(tx *sql.Tx, userID int64) error {
	_, err := tx.Exec(`UPDATE users SET cal_version=cal_version+1 WHERE id=?`, userID)
	return err
}

func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateEvent(userID int64, e *Event) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO events(user_id,start_at,end_at,status,description,auto,updated_at) VALUES(?,?,?,?,?,0,?)`,
			userID, e.Start, e.End, e.Status, e.Description, time.Now().Unix())
		if err != nil {
			return err
		}
		e.ID, _ = res.LastInsertId()
		return bump(tx, userID)
	})
}

func (s *Store) UpdateEvent(userID int64, e *Event) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE events SET start_at=?, end_at=?, status=?, description=?, auto=0, updated_at=? WHERE id=? AND user_id=?`,
			e.Start, e.End, e.Status, e.Description, time.Now().Unix(), e.ID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return bump(tx, userID)
	})
}

func (s *Store) DeleteEvent(userID, id int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM events WHERE id=? AND user_id=?`, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return bump(tx, userID)
	})
}

// ---- live status ----

func (s *Store) Status(userID int64, now int64) (LiveStatus, error) {
	var st LiveStatus
	var running, online int
	err := s.db.QueryRow(`SELECT
			COALESCE(SUM(last_seen >= ?),0),
			COALESCE(SUM(last_seen >= ? AND active=1),0),
			COALESCE(MAX(last_seen),0),
			COALESCE(MAX(last_active),0),
			(SELECT cal_version FROM users WHERE id=?)
		FROM devices WHERE user_id=?`, now-runningWindow, now-runningWindow, userID, userID).
		Scan(&running, &online, &st.LastSeen, &st.LastActive, &st.Version)
	st.Running, st.Online = running > 0, online > 0
	return st, err
}

// Ping records a heartbeat from one PC, syncs the user's Working blocks with the
// combined live status and returns messages that this PC should pop up.
func (s *Store) Ping(userID int64, deviceID string, active bool, now int64) ([]Message, error) {
	var msgs []Message
	err := s.withTx(func(tx *sql.Tx) error {
		act := 0
		lastActive := int64(0)
		if active {
			act, lastActive = 1, now
		}
		if _, err := tx.Exec(`INSERT INTO devices(user_id, device_id, active, last_seen, last_active) VALUES(?,?,?,?,?)
			ON CONFLICT(user_id, device_id) DO UPDATE SET active=excluded.active, last_seen=excluded.last_seen,
			last_active=MAX(devices.last_active, excluded.last_active)`, userID, deviceID, act, now, lastActive); err != nil {
			return err
		}
		var online, activeDevices int
		if err := tx.QueryRow(`SELECT COALESCE(SUM(active),0) FROM devices WHERE user_id=? AND last_seen>=?`,
			userID, now-runningWindow).Scan(&activeDevices); err != nil {
			return err
		}
		if activeDevices > 0 {
			online = 1
		}
		if err := syncWorking(tx, userID, online == 1, now, s.Sync); err != nil {
			return err
		}
		var err error
		msgs, err = takeMessages(tx, userID, deviceID, active, activeDevices > 0, now)
		return err
	})
	return msgs, err
}

// syncWorking makes Working blocks mirror the live status for the time range
// decided since the previous ping. Only the range [min(cursor, now), now+lookahead]
// is touched, so the past before the client started is never modified.
// The cursor advances even when a direction is disabled, so re-enabling it
// never rewrites time that already passed.
func syncWorking(tx *sql.Tx, userID int64, online bool, now int64, opt SyncOptions) error {
	var cursor, syncAt int64
	if err := tx.QueryRow(`SELECT sync_cursor, sync_at FROM users WHERE id=?`, userID).Scan(&cursor, &syncAt); err != nil {
		return err
	}
	from := now
	if now-syncAt <= runningWindow && cursor < now {
		from = cursor
	}
	to := now + lookahead
	var changed bool
	var err error
	switch {
	case online && opt.FillOnline:
		changed, err = fillWorking(tx, userID, from, to, now)
	case !online && opt.EatOffline:
		changed, err = eatWorking(tx, userID, from, to, now)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET sync_cursor=?, sync_at=? WHERE id=?`, to, now, userID); err != nil {
		return err
	}
	if changed {
		return bump(tx, userID)
	}
	return nil
}

type span struct{ id, start, end int64 }

func workingSpans(tx *sql.Tx, userID, from, to int64, inclusive bool) ([]span, error) {
	q := `SELECT id, start_at, end_at FROM events WHERE user_id=? AND status='working' AND start_at < ? AND end_at > ? ORDER BY start_at, id`
	if inclusive {
		q = `SELECT id, start_at, end_at FROM events WHERE user_id=? AND status='working' AND start_at <= ? AND end_at >= ? ORDER BY start_at, id`
	}
	rows, err := tx.Query(q, userID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []span
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.id, &sp.start, &sp.end); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// fillWorking ensures [from,to] is covered by Working blocks, preferring to extend
// an adjacent block over creating a new one.
func fillWorking(tx *sql.Tx, userID, from, to, now int64) (bool, error) {
	spans, err := workingSpans(tx, userID, from, to, true)
	if err != nil {
		return false, err
	}
	changed := false
	setEnd := func(id, end int64) error {
		changed = true
		_, err := tx.Exec(`UPDATE events SET end_at=?, updated_at=? WHERE id=?`, end, now, id)
		return err
	}
	cur := from
	var prev *span
	for i := range spans {
		sp := &spans[i]
		if sp.start > cur {
			if prev != nil && prev.end >= cur {
				if err := setEnd(prev.id, sp.start); err != nil {
					return false, err
				}
				prev.end = sp.start
			} else {
				changed = true
				if _, err := tx.Exec(`UPDATE events SET start_at=?, updated_at=? WHERE id=?`, cur, now, sp.id); err != nil {
					return false, err
				}
				sp.start = cur
			}
		}
		if prev == nil || sp.end >= prev.end {
			prev = sp
		}
		if sp.end > cur {
			cur = sp.end
		}
	}
	if cur < to {
		if prev != nil && prev.end >= cur {
			return true, setEnd(prev.id, to)
		}
		_, err := tx.Exec(`INSERT INTO events(user_id,start_at,end_at,status,description,auto,updated_at) VALUES(?,?,?,'working','',1,?)`,
			userID, cur, to, now)
		return true, err
	}
	return changed, nil
}

// eatWorking removes [from,to] from any Working blocks (trim, split or delete).
func eatWorking(tx *sql.Tx, userID, from, to, now int64) (bool, error) {
	spans, err := workingSpans(tx, userID, from, to, false)
	if err != nil {
		return false, err
	}
	for _, sp := range spans {
		switch {
		case sp.start >= from && sp.end <= to:
			_, err = tx.Exec(`DELETE FROM events WHERE id=?`, sp.id)
		case sp.start < from && sp.end > to:
			if _, err = tx.Exec(`INSERT INTO events(user_id,start_at,end_at,status,description,auto,updated_at)
				SELECT user_id, ?, end_at, status, description, auto, ? FROM events WHERE id=?`, to, now, sp.id); err == nil {
				_, err = tx.Exec(`UPDATE events SET end_at=?, updated_at=? WHERE id=?`, from, now, sp.id)
			}
		case sp.start < from:
			_, err = tx.Exec(`UPDATE events SET end_at=?, updated_at=? WHERE id=?`, from, now, sp.id)
		default:
			_, err = tx.Exec(`UPDATE events SET start_at=?, updated_at=? WHERE id=?`, to, now, sp.id)
		}
		if err != nil {
			return false, err
		}
	}
	return len(spans) > 0, nil
}

// ---- messages ----

func (s *Store) AddMessage(userID int64, sender, body string, now int64) error {
	_, err := s.db.Exec(`INSERT INTO messages(user_id, sender, body, created_at) VALUES(?,?,?,?)`, userID, sender, body, now)
	return err
}

// takeMessages delivers to active PCs; if no PC is active, to any running PC.
// Other PCs can still pick a message up shortly after the first delivery.
func takeMessages(tx *sql.Tx, userID int64, deviceID string, active, anyActive bool, now int64) ([]Message, error) {
	if !active && anyActive {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT id, sender, body, created_at FROM messages m
		WHERE user_id=? AND created_at>=?
		AND (first_delivered_at IS NULL OR first_delivered_at>=?)
		AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.message_id=m.id AND d.device_id=?)
		ORDER BY id LIMIT 5`, userID, now-msgTTL, now-msgFanout, deviceID)
	if err != nil {
		return nil, err
	}
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Sender, &m.Body, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	rows.Close()
	for _, m := range out {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO deliveries(message_id, device_id) VALUES(?,?)`, m.ID, deviceID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE messages SET first_delivered_at=COALESCE(first_delivered_at, ?) WHERE id=?`, now, m.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Cleanup removes stale devices and old messages.
func (s *Store) Cleanup(now int64) {
	s.db.Exec(`DELETE FROM messages WHERE created_at < ?`, now-7*86400)
	s.db.Exec(`DELETE FROM devices WHERE last_seen < ?`, now-60*86400)
}
