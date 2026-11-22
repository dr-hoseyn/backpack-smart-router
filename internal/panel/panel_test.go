package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
)

func TestSQLiteSessionCSRFAndBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "router.sqlite")
	snapshot := filepath.Join(directory, "engine.json")
	cfg, err := router.Load("../../deploy/engine.example.json")
	if err != nil {
		t.Fatal(err)
	}
	db, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	password := "locally-generated-test-password"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap(db, "admin", hash, cfg); err != nil {
		t.Fatal(err)
	}
	p := &panel{db: db, snapshot: snapshot, client: router.ControlClient(filepath.Join(directory, "unavailable.sock")),
		auth:    auth{sessions: map[string]session{}, attempts: map[string]limit{}, slots: make(chan struct{}, 2), duration: time.Hour},
		streams: make(chan struct{}, 1)}
	handle := p.handler(fstest.MapFS{"index.html": {Data: []byte("panel")}})
	var cookie *http.Cookie
	send := func(method, path, body, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		out := httptest.NewRecorder()
		handle.ServeHTTP(out, req)
		return out
	}
	if got := send("GET", "/api/v1/config", "", "").Code; got != 401 {
		t.Fatalf("unauthenticated API response %d", got)
	}
	if got := send("POST", "/api/v1/login", `{"name":"admin","password":"wrong password"}`, "").Code; got != 401 {
		t.Fatalf("bad login response %d", got)
	}
	login := send("POST", "/api/v1/login", `{"name":"admin","password":"`+password+`"}`, "")
	if login.Code != 200 {
		t.Fatalf("login response %d: %s", login.Code, login.Body.String())
	}
	cookie = login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie is missing protection")
	}
	var auth struct{ CSRF string }
	if err := json.Unmarshal(login.Body.Bytes(), &auth); err != nil || auth.CSRF == "" {
		t.Fatal("session missing CSRF token")
	}
	cfg.Strategy = "adaptive"
	data, _ := json.Marshal(cfg)
	if got := send("PUT", "/api/v1/config", string(data), "").Code; got != 403 {
		t.Fatalf("missing CSRF response %d", got)
	}
	if got := send("PUT", "/api/v1/config", string(data), auth.CSRF); got.Code != 202 {
		t.Fatalf("wanted pending configuration, got %d: %s", got.Code, got.Body.String())
	}
	if got := send("PUT", "/api/v1/config", string(data), auth.CSRF).Code; got != 409 {
		t.Fatalf("stale revision response %d", got)
	}
	saved, err := router.Load(snapshot)
	if err != nil || saved.Revision != 2 || saved.Strategy != "adaptive" {
		t.Fatalf("invalid snapshot %+v: %v", saved, err)
	}
	now := time.Now()
	if err := capture(db, router.Status{RXRate: 800, TXRate: 120}, now); err != nil {
		t.Fatal(err)
	}
	points, err := metricHistory(context.Background(), db, 5*time.Minute)
	if err != nil || len(points) != 1 || points[0].RX != 800 {
		t.Fatalf("invalid metrics %+v: %v", points, err)
	}
	backup := filepath.Join(directory, "backup.sqlite")
	if err := BackupDatabase(path, backup); err != nil {
		t.Fatal(err)
	}
	copied, err := openStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	if restored, err := getConfiguration(copied); err != nil || restored.Revision != 2 {
		t.Fatalf("invalid backup %+v: %v", restored, err)
	}
	if got := send("POST", "/api/v1/logout", "", auth.CSRF).Code; got != 204 {
		t.Fatalf("logout response %d", got)
	}
	if got := send("GET", "/api/v1/config", "", "").Code; got != 401 {
		t.Fatalf("revoked session response %d", got)
	}
}
