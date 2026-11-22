package router

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

func (e *Engine) Control(path, configPath string) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("unix", path)
	if err != nil {
		// Only remove a stale socket. Never unlink a running engine or a regular file.
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			conn, dialErr := net.DialTimeout("unix", path, time.Second)
			switch {
			case dialErr == nil:
				conn.Close()
			case errors.Is(dialErr, os.ErrNotExist), isRefused(dialErr):
				if err := os.Remove(path); err != nil {
					return nil, nil, err
				}
				listener, err = net.Listen("unix", path)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, 200, e.Status()) })
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, _ *http.Request) {
		cfg, err := Load(configPath)
		if err == nil && cfg.Revision == e.Status().Revision {
			w.WriteHeader(204)
			return
		}
		if err == nil {
			err = e.Apply(cfg)
		}
		if err != nil {
			WriteJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /probe/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Check(r.Context(), r.PathValue("id")); err != nil {
			WriteJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(204)
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second}, listener, nil
}

func ControlClient(path string) *http.Client {
	return &http.Client{Timeout: 33 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 2 * time.Second}
			return dialer.DialContext(ctx, "unix", path)
		},
	}}
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
