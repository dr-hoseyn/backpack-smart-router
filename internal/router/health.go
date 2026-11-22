package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"sync"
	"time"
)

var probeClient = &http.Client{
	Transport:     &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func probe(ctx context.Context, spec Backend) (Telemetry, error) {
	if spec.TCP != "" {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", spec.TCP)
		if err != nil {
			return Telemetry{}, errors.New("backend TCP port unavailable")
		}
		conn.Close()
	}
	if spec.Probe == "" {
		return Telemetry{}, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+spec.Probe+"/health", nil)
	if err != nil {
		return Telemetry{}, err
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return Telemetry{}, errors.New("VM health agent unavailable")
	}
	defer response.Body.Close()
	var telemetry Telemetry
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&telemetry); err != nil {
		return Telemetry{}, errors.New("invalid VM health report")
	}
	if response.StatusCode != http.StatusOK || !telemetry.Verified ||
		telemetry.ProbeMS == nil || *telemetry.ProbeMS <= 0 || math.IsInf(*telemetry.ProbeMS, 0) {
		return telemetry, errors.New("end-to-end probe failed")
	}
	return telemetry, nil
}

func (e *Engine) Check(ctx context.Context, id string) error {
	e.mu.Lock()
	var target *backend
	for _, list := range e.byGroup {
		for _, b := range list {
			if b.spec.ID == id && b.spec.Enabled {
				target = b
				break
			}
		}
	}
	if target == nil || target.probing {
		e.mu.Unlock()
		return errors.New("backend is absent, disabled, or already being checked")
	}
	target.probing = true
	spec := target.spec
	timeout := time.Duration(e.cfg.CheckTimeoutMS) * time.Millisecond
	e.mu.Unlock()
	before := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	telemetry, err := probe(probeCtx, spec)
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	target.probing = false
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if target.spec.Enabled != spec.Enabled {
		return errors.New("backend changed during probe")
	}
	current := false
	for _, b := range e.byGroup[target.group] {
		if b == target {
			current = true
			break
		}
	}
	if !current {
		return errors.New("backend changed during probe")
	}
	if err != nil {
		target.successes = 0
		target.failures = min(target.failures+1, e.cfg.Failures)
		target.failure = err.Error()
		if target.failures >= e.cfg.Failures && target.health != "unhealthy" {
			target.health = "unhealthy"
			target.recoverAfter = now.Add(time.Duration(e.cfg.CooldownSeconds) * time.Second)
			e.record("unhealthy", spec.ID, target.failure)
		}
		target.telemetry = Telemetry{}
		return err
	}
	target.failures = 0
	target.failure = ""
	target.lastOK = now
	target.telemetry = telemetry
	ms := float64(now.Sub(before)) / float64(time.Millisecond)
	if telemetry.ProbeMS != nil {
		ms = *telemetry.ProbeMS
	}
	if target.rtt == 0 {
		target.rtt = ms
	} else {
		target.rtt = 0.4*ms + 0.6*target.rtt
	}
	target.successes = min(target.successes+1, e.cfg.Recoveries)
	if target.health != "healthy" && target.successes >= e.cfg.Recoveries && !now.Before(target.recoverAfter) {
		target.health = "healthy"
		e.record("healthy", spec.ID, "probe passed")
	}
	return nil
}

func (e *Engine) monitor(ctx context.Context) {
	for ctx.Err() == nil {
		e.mu.Lock()
		interval := time.Duration(e.cfg.CheckIntervalMS) * time.Millisecond
		var ids []string
		for _, list := range e.byGroup {
			for _, b := range list {
				if b.spec.Enabled {
					ids = append(ids, b.spec.ID)
				}
			}
		}
		e.mu.Unlock()
		start := time.Now()
		var wg sync.WaitGroup
		slots := make(chan struct{}, 16)
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			slots <- struct{}{}
			wg.Go(func() { defer func() { <-slots }(); _ = e.Check(ctx, id) })
		}
		wg.Wait()
		timer := time.NewTimer(max(0, interval-time.Since(start)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
