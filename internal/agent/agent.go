package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
	"golang.org/x/net/proxy"
)

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

type sample struct {
	total, idle, segments, retransmits uint64
}

func collect(previous sample, iface string) (router.Telemetry, sample) {
	var telemetry router.Telemetry
	var next sample
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		first, _, _ := strings.Cut(string(raw), "\n")
		fields := strings.Fields(first)
		if len(fields) >= 9 && fields[0] == "cpu" {
			for i, s := range fields[1:9] {
				n, _ := strconv.ParseUint(s, 10, 64)
				next.total += n
				if i == 3 || i == 4 {
					next.idle += n
				}
			}
			if previous.total > 0 && next.total > previous.total && next.idle >= previous.idle {
				cpu := 100 * (1 - float64(next.idle-previous.idle)/float64(next.total-previous.total))
				cpu = max(0, min(cpu, 100))
				telemetry.CPU = &cpu
			}
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available uint64
		for line := range strings.SplitSeq(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				total, _ = strconv.ParseUint(fields[1], 10, 64)
			case "MemAvailable:":
				available, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
		if total > 0 && available > 0 && total >= available {
			usage := 100 * float64(total-available) / float64(total)
			telemetry.RAM = &usage
		}
	}
	if raw, err := os.ReadFile("/proc/net/snmp"); err == nil {
		var headers []string
		for line := range strings.SplitSeq(string(raw), "\n") {
			if !strings.HasPrefix(line, "Tcp: ") {
				continue
			}
			fields := strings.Fields(line)
			if headers == nil {
				headers = fields
				continue
			}
			if len(fields) != len(headers) {
				break
			}
			for i, header := range headers {
				switch header {
				case "OutSegs":
					next.segments, _ = strconv.ParseUint(fields[i], 10, 64)
				case "RetransSegs":
					next.retransmits, _ = strconv.ParseUint(fields[i], 10, 64)
				}
			}
			break
		}
		if next.segments >= previous.segments && next.retransmits >= previous.retransmits {
			sent, retransmitted := next.segments-previous.segments, next.retransmits-previous.retransmits
			if previous.segments > 0 && sent+retransmitted > 0 {
				ratio := 100 * float64(retransmitted) / float64(sent+retransmitted)
				telemetry.Retransmits = &ratio
			}
		}
	}
	if iface != "" {
		read := func(counter string) *uint64 {
			raw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", counter))
			if err != nil {
				return nil
			}
			value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil {
				return nil
			}
			return &value
		}
		telemetry.InterfaceRX, telemetry.InterfaceTX = read("rx_bytes"), read("tx_bytes")
	}
	return telemetry, next
}

func Serve(ctx context.Context, listen, socks, target, iface string) error {
	destination, err := url.Parse(target)
	if err != nil || destination.Scheme != "https" || destination.Hostname() == "" || destination.User != nil || destination.Fragment != "" {
		return errors.New("target must be an HTTPS URL without credentials")
	}
	socksHost, _, err := net.SplitHostPort(socks)
	if err != nil || !net.ParseIP(socksHost).IsLoopback() {
		return errors.New("SOCKS endpoint must be a loopback IP and port")
	}
	listenHost, _, err := net.SplitHostPort(listen)
	listenIP := net.ParseIP(listenHost)
	if err != nil || (!listenIP.IsLoopback() && !listenIP.IsPrivate()) {
		return errors.New("agent must bind a private or loopback IP")
	}
	if iface != "" && !interfaceName.MatchString(iface) {
		return errors.New("invalid network interface")
	}
	dialer, err := proxy.SOCKS5("tcp", socks, nil, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext, TLSHandshakeTimeout: 3 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var mu sync.RWMutex
	var telemetry router.Telemetry
	var updated time.Time
	go func() {
		var previous sample
		for ctx.Err() == nil {
			var current router.Telemetry
			current, previous = collect(previous, iface)
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			started := time.Now()
			response, err := client.Do(request)
			if err == nil {
				current.Verified = response.StatusCode == http.StatusNoContent
				elapsed := float64(time.Since(started)) / float64(time.Millisecond)
				current.ProbeMS = &elapsed
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
			}
			mu.Lock()
			telemetry, updated = current, time.Now()
			mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		value, age := telemetry, time.Since(updated)
		mu.RUnlock()
		code := http.StatusOK
		if !value.Verified || age > 10*time.Second {
			value.Verified = false
			code = http.StatusServiceUnavailable
		}
		router.WriteJSON(w, code, value)
	})
	server := http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	stop := context.AfterFunc(ctx, func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})
	defer stop()
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
