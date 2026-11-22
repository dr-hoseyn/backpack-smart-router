package panel

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
)

type Options struct {
	Address, Socket, Snapshot, Database, Assets, Admin, HashFile string
	TLSCert, TLSKey                                              string
	SessionMinutes, RetentionDays                                int
}

type panel struct {
	db        *sql.DB
	client    *http.Client
	auth      auth
	snapshot  string
	retention time.Duration
	mu        sync.Mutex
	status    atomic.Pointer[liveStatus]
	streams   chan struct{}
}

type liveStatus struct {
	Online bool      `json:"online"`
	At     time.Time `json:"at"`
	router.Status
}

func Serve(ctx context.Context, options Options) error {
	if options.SessionMinutes < 5 || options.SessionMinutes > 1440 || options.RetentionDays < 7 || options.RetentionDays > 365 {
		return errors.New("invalid session or retention duration")
	}
	secure := options.TLSCert != "" && options.TLSKey != ""
	if (options.TLSCert == "") != (options.TLSKey == "") {
		return errors.New("TLS requires a certificate and key")
	}
	host, _, err := net.SplitHostPort(options.Address)
	ip := net.ParseIP(host)
	if err != nil || (!ip.IsLoopback() && (!ip.IsPrivate() || !secure)) {
		return errors.New("management requires a loopback address or a private address with TLS")
	}
	initial, err := router.Load(options.Snapshot)
	if err != nil {
		return err
	}
	db, err := openStore(options.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	hash, err := os.ReadFile(options.HashFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := bootstrap(db, options.Admin, strings.TrimSpace(string(hash)), initial); err != nil {
		return err
	}
	ui := os.DirFS(options.Assets)
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return fmt.Errorf("dashboard assets: %w", err)
	}
	p := &panel{db: db, client: router.ControlClient(options.Socket), snapshot: options.Snapshot,
		retention: time.Duration(options.RetentionDays) * 24 * time.Hour, streams: make(chan struct{}, 48),
		auth: auth{sessions: map[string]session{}, attempts: map[string]limit{}, slots: make(chan struct{}, 2),
			duration: time.Duration(options.SessionMinutes) * time.Minute, secure: secure}}
	server := &http.Server{Addr: options.Address, Handler: p.handler(ui), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Go(func() { p.monitor(ctx) })
	defer func() { cancel(); workers.Wait() }()
	stop := context.AfterFunc(ctx, func() {
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	})
	defer stop()
	if secure {
		err = server.ListenAndServeTLS(options.TLSCert, options.TLSKey)
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (p *panel) control(ctx context.Context, method, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://router"+path, nil)
	if err != nil {
		return err
	}
	response, err := p.client.Do(request)
	if err != nil {
		return errors.New("engine unavailable")
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, 2<<20)
	if response.StatusCode >= 300 {
		var failure struct{ Error string }
		if json.NewDecoder(reader).Decode(&failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return errors.New("engine rejected request")
	}
	if result != nil {
		return json.NewDecoder(reader).Decode(result)
	}
	return nil
}

func (p *panel) reconcile(ctx context.Context, revision uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg, err := getConfiguration(p.db)
	if err != nil {
		return err
	}
	if cfg.Revision == revision {
		return nil
	}
	if err := router.Save(p.snapshot, cfg); err != nil {
		return err
	}
	return p.control(ctx, http.MethodPost, "/reload", nil)
}

func (p *panel) monitor(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastCapture, lastCleanup time.Time
	for ctx.Err() == nil {
		now := time.Now()
		query, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		var status router.Status
		err := p.control(query, http.MethodGet, "/status", &status)
		if err == nil {
			p.status.Store(&liveStatus{Online: true, At: now, Status: status})
			if err := p.reconcile(query, status.Revision); err != nil {
				slog.Warn("configuration pending", "error", err)
			}
			if now.Sub(lastCapture) >= 5*time.Second {
				if err := capture(p.db, status, now); err != nil {
					slog.Error("capture metrics", "error", err)
				} else {
					lastCapture = now
				}
			}
		} else {
			p.status.Store(&liveStatus{At: now})
		}
		cancel()
		if now.Sub(lastCleanup) >= time.Hour {
			cutoff := now.Add(-p.retention).Unix()
			_, metricErr := p.db.Exec("DELETE FROM metrics WHERE ts < ?", cutoff)
			_, eventErr := p.db.Exec("DELETE FROM events WHERE ts < ?", cutoff)
			if err := errors.Join(metricErr, eventErr); err != nil {
				slog.Error("retention", "error", err)
			} else {
				lastCleanup = now
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func fail(w http.ResponseWriter, code int, err error) {
	router.WriteJSON(w, code, map[string]string{"error": err.Error()})
}

func readBody(w http.ResponseWriter, r *http.Request, dest any, limit int64) error {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/json" {
		return errors.New("application/json required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}

type protected func(http.ResponseWriter, *http.Request, session, string)

func (p *panel) require(handler protected) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, id, ok := p.auth.lookup(r)
		if !ok {
			fail(w, 401, errors.New("authentication required"))
			return
		}
		if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(session.csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			fail(w, 403, errors.New("invalid CSRF token"))
			return
		}
		handler(w, r, session, id)
	}
}

func (p *panel) handler(ui fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("POST /api/v1/login", p.login)
	mux.HandleFunc("GET /api/v1/session", p.require(func(w http.ResponseWriter, _ *http.Request, s session, _ string) {
		router.WriteJSON(w, 200, map[string]string{"name": s.name, "csrf": s.csrf})
	}))
	mux.HandleFunc("POST /api/v1/logout", p.require(func(w http.ResponseWriter, _ *http.Request, _ session, id string) {
		p.auth.revoke(id, "")
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: p.auth.secure, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/status", p.require(func(w http.ResponseWriter, _ *http.Request, _ session, _ string) {
		status := p.status.Load()
		if status == nil {
			status = &liveStatus{}
		}
		router.WriteJSON(w, 200, status)
	}))
	mux.HandleFunc("GET /api/v1/config", p.require(func(w http.ResponseWriter, _ *http.Request, _ session, _ string) {
		cfg, err := getConfiguration(p.db)
		if err != nil {
			fail(w, 500, errors.New("configuration unavailable"))
			return
		}
		router.WriteJSON(w, 200, cfg)
	}))
	mux.HandleFunc("PUT /api/v1/config", p.require(p.updateConfiguration))
	mux.HandleFunc("POST /api/v1/backends/{id}/check", p.require(p.check))
	mux.HandleFunc("GET /api/v1/metrics", p.require(p.metrics))
	mux.HandleFunc("GET /api/v1/events", p.require(p.events))
	mux.HandleFunc("GET /api/v1/live", p.require(p.live))
	mux.HandleFunc("GET /api/v1/admins", p.require(p.admins))
	mux.HandleFunc("POST /api/v1/admins", p.require(p.addAdmin))
	mux.HandleFunc("DELETE /api/v1/admins/{name}", p.require(p.deleteAdmin))
	mux.HandleFunc("PUT /api/v1/password", p.require(p.changePassword))
	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if p.auth.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	}))
}

func (p *panel) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Password string }
	if err := readBody(w, r, &body, 1024); err != nil || len(body.Password) > 256 || !adminName.MatchString(body.Name) {
		fail(w, 400, errors.New("invalid credentials format"))
		return
	}
	if !p.auth.allowed(r) {
		fail(w, 429, errors.New("too many attempts"))
		return
	}
	select {
	case p.auth.slots <- struct{}{}:
		defer func() { <-p.auth.slots }()
	default:
		fail(w, 429, errors.New("authentication busy"))
		return
	}
	var hash string
	_ = p.db.QueryRow("SELECT hash FROM admins WHERE name=?", body.Name).Scan(&hash)
	if !verify(hash, body.Password) {
		fail(w, 401, errors.New("invalid credentials"))
		return
	}
	_, oldSession, _ := p.auth.lookup(r)
	p.auth.revoke(oldSession, "")
	s, ok := p.auth.start(w, body.Name)
	if !ok {
		fail(w, 429, errors.New("session limit reached"))
		return
	}
	_ = audit(p.db, s.name, "login", "", "signed in")
	router.WriteJSON(w, 200, map[string]string{"name": s.name, "csrf": s.csrf})
}

func (p *panel) updateConfiguration(w http.ResponseWriter, r *http.Request, s session, _ string) {
	cfg, err := router.Decode(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := cfg.ValidateLocal(); err != nil {
		fail(w, 400, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	current, err := getConfiguration(p.db)
	if err != nil {
		fail(w, 500, errors.New("configuration unavailable"))
		return
	}
	if current.Revision != cfg.Revision || !router.SameListeners(current, cfg) {
		fail(w, 409, errors.New("stale revision or changed listener; reload settings"))
		return
	}
	cfg.Revision++
	if err := saveConfiguration(p.db, cfg, s.name); err != nil {
		fail(w, 500, errors.New("could not commit configuration"))
		return
	}
	if err := router.Save(p.snapshot, cfg); err != nil {
		slog.Error("write engine snapshot", "error", err)
		router.WriteJSON(w, 202, cfg)
		return
	}
	reload, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	code := 200
	if err := p.control(reload, http.MethodPost, "/reload", nil); err != nil {
		code = 202
	}
	router.WriteJSON(w, code, cfg)
}

func (p *panel) check(w http.ResponseWriter, r *http.Request, s session, _ string) {
	id := r.PathValue("id")
	if len(id) > 63 || strings.ContainsAny(id, "/?#%") {
		fail(w, 400, errors.New("invalid backend ID"))
		return
	}
	err := p.control(r.Context(), http.MethodPost, "/probe/"+id, nil)
	detail := "probe passed"
	if err != nil {
		detail = "probe failed"
	}
	_ = audit(p.db, s.name, "manual-check", id, detail)
	if err != nil {
		fail(w, 409, err)
		return
	}
	w.WriteHeader(204)
}

func (p *panel) metrics(w http.ResponseWriter, r *http.Request, _ session, _ string) {
	span := r.URL.Query().Get("range")
	durations := map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}
	if span == "" {
		span = "5m"
	}
	duration, ok := durations[span]
	if !ok {
		fail(w, 400, errors.New("invalid range"))
		return
	}
	points, err := metricHistory(r.Context(), p.db, duration)
	if err != nil {
		fail(w, 500, errors.New("metrics unavailable"))
		return
	}
	router.WriteJSON(w, 200, points)
}

func (p *panel) events(w http.ResponseWriter, r *http.Request, _ session, _ string) {
	query := r.URL.Query()
	duration := 24 * time.Hour
	if value := query.Get("hours"); value != "" {
		hours, err := strconv.Atoi(value)
		if err != nil || hours < 1 || hours > 8760 {
			fail(w, 400, errors.New("invalid time range"))
			return
		}
		duration = time.Duration(hours) * time.Hour
	}
	items, err := events(r.Context(), p.db, query.Get("kind"), query.Get("backend"), time.Now().Add(-duration).Unix())
	if err != nil {
		fail(w, 500, errors.New("events unavailable"))
		return
	}
	router.WriteJSON(w, 200, items)
}

func (p *panel) live(w http.ResponseWriter, r *http.Request, _ session, _ string) {
	select {
	case p.streams <- struct{}{}:
		defer func() { <-p.streams }()
	default:
		fail(w, 429, errors.New("stream limit reached"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last *liveStatus
	for {
		if _, _, ok := p.auth.lookup(r); !ok {
			return
		}
		if status := p.status.Load(); status != nil && status != last {
			data, err := json.Marshal(status)
			if err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
			last = status
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *panel) admins(w http.ResponseWriter, _ *http.Request, _ session, _ string) {
	rows, err := p.db.Query("SELECT name FROM admins ORDER BY name")
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			fail(w, 500, errors.New("accounts unavailable"))
			return
		}
		names = append(names, name)
	}
	if rows.Err() != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	router.WriteJSON(w, 200, names)
}

func (p *panel) addAdmin(w http.ResponseWriter, r *http.Request, s session, _ string) {
	var body struct{ Name, Password string }
	if err := readBody(w, r, &body, 1024); err != nil || !adminName.MatchString(body.Name) {
		fail(w, 400, errors.New("invalid account"))
		return
	}
	select {
	case p.auth.slots <- struct{}{}:
		defer func() { <-p.auth.slots }()
	default:
		fail(w, 429, errors.New("authentication busy"))
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		fail(w, 400, err)
		return
	}
	tx, err := p.db.Begin()
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO admins SELECT ?,? WHERE (SELECT count(*) FROM admins) < 32", body.Name, hash)
	if err != nil {
		fail(w, 409, errors.New("account already exists or could not be created"))
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		fail(w, 409, errors.New("admin limit reached"))
		return
	}
	if err := audit(tx, s.name, "admin-created", "", body.Name); err != nil {
		fail(w, 500, errors.New("audit unavailable"))
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	w.WriteHeader(204)
}

func (p *panel) deleteAdmin(w http.ResponseWriter, r *http.Request, s session, _ string) {
	name := r.PathValue("name")
	if name == s.name {
		fail(w, 409, errors.New("cannot remove your own account"))
		return
	}
	tx, err := p.db.Begin()
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM admins WHERE name=? AND (SELECT count(*) FROM admins)>1", name)
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		fail(w, 404, errors.New("account unavailable"))
		return
	}
	if err := audit(tx, s.name, "admin-removed", "", name); err != nil {
		fail(w, 500, errors.New("audit unavailable"))
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	p.auth.revoke("", name)
	w.WriteHeader(204)
}

func (p *panel) changePassword(w http.ResponseWriter, r *http.Request, s session, _ string) {
	var body struct{ Current, Password string }
	if err := readBody(w, r, &body, 1024); err != nil || len(body.Current) > 256 {
		fail(w, 400, errors.New("invalid password format"))
		return
	}
	select {
	case p.auth.slots <- struct{}{}:
		defer func() { <-p.auth.slots }()
	default:
		fail(w, 429, errors.New("authentication busy"))
		return
	}
	var old string
	if err := p.db.QueryRow("SELECT hash FROM admins WHERE name=?", s.name).Scan(&old); err != nil || !verify(old, body.Current) {
		fail(w, 401, errors.New("current password does not match"))
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		fail(w, 400, err)
		return
	}
	tx, err := p.db.Begin()
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE admins SET hash=? WHERE name=? AND hash=?", hash, s.name, old)
	if err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		fail(w, 409, errors.New("account changed concurrently"))
		return
	}
	if err := audit(tx, s.name, "password-changed", "", "password changed"); err != nil {
		fail(w, 500, errors.New("audit unavailable"))
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, 500, errors.New("accounts unavailable"))
		return
	}
	p.auth.revoke("", s.name)
	w.WriteHeader(204)
}
