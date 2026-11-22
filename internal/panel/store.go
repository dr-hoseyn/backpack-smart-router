package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS configuration (id INTEGER PRIMARY KEY CHECK (id=1), json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS admins (name TEXT PRIMARY KEY, hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS metrics (
  ts INTEGER PRIMARY KEY, rx REAL NOT NULL, tx REAL NOT NULL, active INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, source TEXT NOT NULL,
  source_id INTEGER NOT NULL, kind TEXT NOT NULL, backend TEXT NOT NULL, detail TEXT NOT NULL,
  UNIQUE(source,source_id)
);
CREATE INDEX IF NOT EXISTS events_time ON events(ts);
`

func openStore(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func getConfiguration(db *sql.DB) (router.Config, error) {
	var cfg router.Config
	var data []byte
	if err := db.QueryRow("SELECT json FROM configuration WHERE id=1").Scan(&data); err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func saveConfiguration(db *sql.DB, cfg router.Config, actor string) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO configuration VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET json=excluded.json", data); err != nil {
		return err
	}
	if err := audit(tx, actor, "configuration", "", "saved configuration"); err != nil {
		return err
	}
	return tx.Commit()
}

type executor interface {
	Exec(string, ...any) (sql.Result, error)
}

func audit(db executor, actor, kind, backend, detail string) error {
	now := time.Now()
	_, err := db.Exec("INSERT INTO events(ts,source,source_id,kind,backend,detail) VALUES(?,?,?,?,?,?)", now.Unix(), actor, now.UnixNano(), kind, backend, detail)
	return err
}

func capture(db *sql.DB, status router.Status, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT OR REPLACE INTO metrics VALUES(?,?,?,?)", now.Unix(), status.RXRate, status.TXRate, status.Active); err != nil {
		return err
	}
	source := "engine:" + status.Started.UTC().Format(time.RFC3339Nano)
	for _, event := range status.Events {
		if _, err := tx.Exec("INSERT OR IGNORE INTO events(ts,source,source_id,kind,backend,detail) VALUES(?,?,?,?,?,?)",
			event.Time.Unix(), source, event.ID, event.Kind, event.Backend, event.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type point struct {
	Time   int64   `json:"time"`
	RX     float64 `json:"rx"`
	TX     float64 `json:"tx"`
	Active float64 `json:"active"`
}

func metricHistory(ctx context.Context, db *sql.DB, age time.Duration) ([]point, error) {
	interval := max(int64(1), int64(age.Seconds()/240))
	rows, err := db.QueryContext(ctx, `SELECT (ts / ?) * ? AS bucket, avg(rx),avg(tx),avg(active)
		FROM metrics WHERE ts >= ? GROUP BY bucket ORDER BY bucket`, interval, interval, time.Now().Add(-age).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []point{}
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.Time, &p.RX, &p.TX, &p.Active); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

type auditEntry struct {
	ID      int64  `json:"id"`
	Time    int64  `json:"time"`
	Actor   string `json:"actor"`
	Kind    string `json:"kind"`
	Backend string `json:"backend"`
	Detail  string `json:"detail"`
}

func events(ctx context.Context, db *sql.DB, kind, backend string, after int64) ([]auditEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,ts,source,kind,backend,detail FROM events
		WHERE ts >= ? AND (? = '' OR kind = ?) AND (? = '' OR backend = ?) ORDER BY id DESC LIMIT 250`,
		after, kind, kind, backend, backend)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auditEntry{}
	for rows.Next() {
		var e auditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.Actor, &e.Kind, &e.Backend, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func bootstrap(db *sql.DB, name, hash string, initial router.Config) error {
	if _, err := getConfiguration(db); errors.Is(err, sql.ErrNoRows) {
		if err := saveConfiguration(db, initial, "bootstrap"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM admins").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if !adminName.MatchString(name) || !validHash(hash) {
			return errors.New("valid initial admin name and Argon2id hash required")
		}
		_, err := db.Exec("INSERT INTO admins VALUES(?,?)", name, hash)
		return err
	}
	return nil
}

func BackupDatabase(database, destination string) error {
	if destination == "" || database == destination {
		return errors.New("invalid backup destination")
	}
	if _, err := os.Stat(database); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	copied := false
	defer func() {
		if !copied {
			_ = os.Remove(destination)
		}
	}()
	db, err := openStore(database)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM INTO ?", destination); err != nil {
		return err
	}
	copied = true
	return nil
}

func RestoreConfiguration(database, snapshot, source string) error {
	cfg, err := router.Load(source)
	if err != nil {
		return err
	}
	if err := cfg.ValidateLocal(); err != nil {
		return err
	}
	current, err := router.Load(snapshot)
	if err != nil {
		return err
	}
	db, err := openStore(database)
	if err != nil {
		return err
	}
	defer db.Close()
	stored, err := getConfiguration(db)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cfg.Revision = max(current.Revision, stored.Revision) + 1
	if err := saveConfiguration(db, cfg, "local-maintenance"); err != nil {
		return err
	}
	return router.Save(snapshot, cfg)
}
