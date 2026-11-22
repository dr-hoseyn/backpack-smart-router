package router

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Revision: 1, Strategy: "weighted-least", MaxFlows: 10000, DialTimeoutMS: 250, TCPIdleSeconds: 10,
		UDPIdleSeconds: 2, CheckIntervalMS: 500, CheckTimeoutMS: 400, Failures: 1, Recoveries: 1,
		Groups: []Group{{ID: "germany", TCP: "127.0.0.1:14443"}}}
}

func tcpTarget(tb testing.TB, marker byte) net.Listener {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if marker != 0 {
					if _, err := conn.Write([]byte{marker}); err != nil {
						return
					}
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

func udpTarget(tb testing.TB) *net.UDPConn {
	tb.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { listener.Close() })
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, client, err := listener.ReadFromUDPAddrPort(buffer)
			if err != nil {
				return
			}
			_, _ = listener.WriteToUDPAddrPort(buffer[:n], client)
		}
	}()
	return listener
}

func allocateTCP(tb testing.TB) string {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func allocateUDP(tb testing.TB) string {
	tb.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	address := listener.LocalAddr().String()
	_ = listener.Close()
	return address
}

func spec(id, target string, weight int) Backend {
	return Backend{ID: id, Name: id, Weight: weight, Capacity: 10000, TCP: target, Enabled: true}
}

func waitFor(tb testing.TB, condition func() bool) {
	tb.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			tb.Fatal("condition did not become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func running(tb testing.TB, cfg Config) *Engine {
	tb.Helper()
	e, err := New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Serve(ctx) }()
	tb.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			tb.Error(err)
		}
	})
	waitFor(tb, func() bool {
		for _, b := range e.Status().Backends {
			if b.State != "healthy" {
				return false
			}
		}
		return true
	})
	return e
}

func echo(tb testing.TB, conn net.Conn, payload []byte) {
	tb.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		tb.Fatal(err)
	}
	data := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != string(payload) {
		tb.Fatalf("wanted %q, received %q (%v)", payload, data, err)
	}
}

func connection(tb testing.TB, protocol, address string) net.Conn {
	tb.Helper()
	conn, err := net.DialTimeout(protocol, address, time.Second)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	return conn
}

func marker(tb testing.TB, conn net.Conn) byte {
	tb.Helper()
	var data [1]byte
	if _, err := io.ReadFull(conn, data[:]); err != nil {
		tb.Fatal(err)
	}
	return data[0]
}

func TestForwardTCPUDPAndDrain(t *testing.T) {
	first, second := tcpTarget(t, 'A'), tcpTarget(t, 'B')
	udpA, udpB := udpTarget(t), udpTarget(t)
	cfg := testConfig()
	cfg.Groups[0].TCP, cfg.Groups[0].UDP = allocateTCP(t), allocateUDP(t)
	a, b := spec("a", first.Addr().String(), 100), spec("b", second.Addr().String(), 1)
	a.UDP, b.UDP = udpA.LocalAddr().String(), udpB.LocalAddr().String()
	cfg.Groups[0].Backends = []Backend{a, b}
	e := running(t, cfg)
	old := connection(t, "tcp", cfg.Groups[0].TCP)
	if got := marker(t, old); got != 'A' {
		t.Fatalf("expected A, got %c", got)
	}
	udp := connection(t, "udp", cfg.Groups[0].UDP)
	echo(t, udp, []byte("first datagram"))
	next := cfg
	next.Groups = append([]Group{}, cfg.Groups...)
	next.Groups[0].Backends = []Backend{a, b}
	next.Groups[0].Backends[0].Drain = true
	next.Revision++
	if err := e.Apply(next); err != nil {
		t.Fatal(err)
	}
	post := connection(t, "tcp", cfg.Groups[0].TCP)
	if got := marker(t, post); got != 'B' {
		t.Fatalf("expected B, got %c", got)
	}
	echo(t, old, []byte("existing TCP"))
	echo(t, udp, []byte("existing UDP"))
	fresh := connection(t, "udp", cfg.Groups[0].UDP)
	echo(t, fresh, []byte("new UDP"))
	state := e.Status()
	if state.Backends[0].UDPActive != 1 || state.Backends[1].UDPActive != 1 || state.RX == 0 || state.TX == 0 {
		t.Fatalf("unexpected forwarding state: %+v", state.Backends)
	}
	if err := old.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("wanted half-close EOF, got %v", err)
	}
	waitFor(t, func() bool { return e.Status().Backends[0].UDPActive == 0 })
}

func TestEndToEndFailureWithOpenPort(t *testing.T) {
	first, second := tcpTarget(t, 'A'), tcpTarget(t, 'B')
	var healthy atomic.Bool
	healthy.Store(true)
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
		}
		probeMS := 17.0
		_ = json.NewEncoder(w).Encode(Telemetry{Verified: healthy.Load(), ProbeMS: &probeMS})
	}))
	defer probe.Close()
	cfg := testConfig()
	cfg.Groups[0].TCP = allocateTCP(t)
	a, b := spec("a", first.Addr().String(), 100), spec("b", second.Addr().String(), 1)
	a.Probe = strings.TrimPrefix(probe.URL, "http://")
	cfg.Groups[0].Backends = []Backend{a, b}
	e := running(t, cfg)
	healthy.Store(false)
	waitFor(t, func() bool { return e.Status().Backends[0].State == "unhealthy" })
	client := connection(t, "tcp", cfg.Groups[0].TCP)
	if got := marker(t, client); got != 'B' {
		t.Fatalf("expected fallback B, got %c", got)
	}
	healthy.Store(true)
	waitFor(t, func() bool { return e.Status().Backends[0].State == "healthy" })
	client = connection(t, "tcp", cfg.Groups[0].TCP)
	if got := marker(t, client); got != 'A' {
		t.Fatalf("expected recovery A, got %c", got)
	}
	if status := e.Status(); len(status.Events) < 5 || !status.Backends[0].Verified || status.Backends[0].RTT != 17.0 {
		t.Fatalf("missing probe or transition information: %+v", status)
	}
}

func six() Config {
	cfg := testConfig()
	for i := 1; i <= 6; i++ {
		cfg.Groups[0].Backends = append(cfg.Groups[0].Backends, spec("vm-"+strconv.Itoa(i), "127.0.0.1:18888", i))
	}
	return cfg
}

func healthyEngine(t testing.TB, cfg Config) *Engine {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range e.byGroup["germany"] {
		b.health = "healthy"
	}
	return e
}

func TestStrategiesAndConcurrentCapacity(t *testing.T) {
	cfg := six()
	cfg.Strategy = "weighted-round-robin"
	e := healthyEngine(t, cfg)
	counts := map[string]int{}
	for range 210 {
		r := e.choose("germany", false, nil)
		counts[r.b.spec.ID]++
		e.release(r.b, false)
	}
	for _, b := range cfg.Groups[0].Backends {
		if counts[b.ID] != b.Weight*10 {
			t.Fatalf("weighted distribution: %v", counts)
		}
	}
	cfg.MaxFlows = 100
	cfg.Strategy = "weighted-least"
	e = healthyEngine(t, cfg)
	var group sync.WaitGroup
	active := make(chan *backend, 200)
	for range 200 {
		group.Go(func() {
			if r := e.choose("germany", false, nil); r.b != nil {
				active <- r.b
			}
		})
	}
	group.Wait()
	close(active)
	if got := len(active); got != 100 {
		t.Fatalf("expected 100 reservations, got %d", got)
	}
	for backend := range active {
		e.release(backend, false)
	}
	if e.Status().Active != 0 {
		t.Fatal("reservations leaked")
	}
	for _, strategy := range []string{"adaptive", "least-rtt", "least-load"} {
		t.Run(strategy, func(t *testing.T) {
			cfg.Strategy = strategy
			e := healthyEngine(t, cfg)
			for i, backend := range e.byGroup["germany"] {
				backend.rtt = float64(10 - i)
				backend.tcpActive = 10 - i
			}
			res := e.choose("germany", false, nil)
			if res.b.spec.ID != "vm-6" {
				t.Fatalf("unexpected decision under %s: %s", strategy, res.b.spec.ID)
			}
		})
	}
}

func TestZeroFlowLimitsDisableAdmissionCaps(t *testing.T) {
	cfg := six()
	cfg.MaxFlows = 0
	cfg.Strategy = "adaptive"
	cfg.Groups[0].Backends = cfg.Groups[0].Backends[:1]
	cfg.Groups[0].Backends[0].Capacity = 0
	e := healthyEngine(t, cfg)
	e.active = 200_000
	e.admitted = 200_000
	e.byGroup["germany"][0].tcpActive = 200_000
	if !e.admit() {
		t.Fatal("zero global limit blocked admission")
	}
	res := e.choose("germany", false, nil)
	if res.b == nil || math.IsInf(res.score, 0) || math.IsNaN(res.score) {
		t.Fatalf("unexpected reservation for unlimited backend: %+v", res)
	}
	e.release(res.b, false)
	e.unadmit()
	if e.active != 200_000 || e.admitted != 200_000 {
		t.Fatal("reservation accounting was not restored")
	}
}

func TestSnapshotAndValidation(t *testing.T) {
	cfg := six()
	path := t.TempDir() + "/engine.json"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Revision++
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if result, err := Load(path); err != nil || result.Revision != cfg.Revision {
		t.Fatalf("snapshot read: %+v %v", result, err)
	}
	cfg.Groups[0].Backends[0].TCP = "203.0.113.8:14443"
	if err := Save(path, cfg); err == nil {
		t.Fatal("accepted public backend")
	}
	if result, err := Load(path); err != nil || result.Groups[0].Backends[0].TCP != "127.0.0.1:18888" {
		t.Fatalf("invalid write replaced active config: %v", err)
	}
}

type retryTestListener struct {
	net.Listener
	attempts int
}

func (listener *retryTestListener) Accept() (net.Conn, error) {
	listener.attempts++
	if listener.attempts == 1 {
		return nil, &net.DNSError{Err: "temporary test error", IsTemporary: true}
	}
	return nil, net.ErrClosed
}

func TestAcceptRetriesTemporaryError(t *testing.T) {
	var engine Engine
	var sessions sync.WaitGroup
	listener := new(retryTestListener)
	if err := engine.acceptTCP(t.Context(), listener, "test", &sessions); err != nil {
		t.Fatal(err)
	}
	if listener.attempts != 2 {
		t.Fatalf("expected accept to retry, got %d attempts", listener.attempts)
	}
}

func TestConcurrentForwarding(t *testing.T) {
	count := 96
	if input := os.Getenv("BACKPACK_TEST_CONNECTIONS"); input != "" {
		var err error
		count, err = strconv.Atoi(input)
		if err != nil || count < 1 || count > 5000 {
			t.Fatal("BACKPACK_TEST_CONNECTIONS must be 1–5000")
		}
	}
	cfg := testConfig()
	cfg.TCPIdleSeconds = 90
	cfg.DialTimeoutMS = 1500
	cfg.Failures = 3
	cfg.Groups[0].TCP = allocateTCP(t)
	for i := 1; i <= 6; i++ {
		back := tcpTarget(t, 0)
		cfg.Groups[0].Backends = append(cfg.Groups[0].Backends, spec("load-"+strconv.Itoa(i), back.Addr().String(), 1))
	}
	e := running(t, cfg)
	clients := make([]net.Conn, count)
	defer func() {
		for _, conn := range clients {
			if conn != nil {
				conn.Close()
			}
		}
	}()
	var wg sync.WaitGroup
	parallel := 24
	if runtime.GOOS == "windows" {
		parallel = 4
	}
	limit := make(chan struct{}, parallel)
	var dialFailures atomic.Uint64
	var firstDialError atomic.Pointer[error]
	for i := range clients {
		if runtime.GOOS == "windows" && i%8 == 0 {
			time.Sleep(3 * time.Millisecond)
		}
		limit <- struct{}{}
		wg.Go(func() {
			defer func() { <-limit }()
			var err error
			for attempt := range 4 {
				clients[i], err = net.DialTimeout("tcp", cfg.Groups[0].TCP, 3*time.Second)
				if err == nil {
					return
				}
				time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
			}
			dialFailures.Add(1)
			firstDialError.CompareAndSwap(nil, &err)
		})
	}
	wg.Wait()
	if failed := dialFailures.Load(); failed > 0 {
		t.Fatalf("%d/%d connections failed (router active %d): %v", failed, count, e.Status().Active, *firstDialError.Load())
	}
	waitFor(t, func() bool { return e.Status().Active == count })
	payload := make([]byte, 32*1024)
	payload[0] = 42
	started := time.Now()
	for _, client := range clients {
		wg.Go(func() {
			_ = client.SetDeadline(time.Now().Add(15 * time.Second))
			if _, err := client.Write(payload); err != nil {
				t.Error(err)
				return
			}
			var result [32 * 1024]byte
			if _, err := io.ReadFull(client, result[:]); err != nil || result[0] != 42 {
				t.Errorf("echo failed: %v", err)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(started)
	t.Logf("%d simultaneous TCP connections, %.2f Gbps aggregate local echo", count, float64(count*len(payload)*2*8)/elapsed.Seconds()/1e9)
	for _, backend := range e.Status().Backends {
		if backend.TCPActive < count/6-1 || backend.TCPActive > count/6+1 {
			t.Fatalf("unbalanced simultaneous flow count: %s has %d of %d", backend.ID, backend.TCPActive, count)
		}
	}
}

func BenchmarkSelectSix(b *testing.B) {
	e := healthyEngine(b, six())
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			res := e.choose("germany", false, nil)
			e.release(res.b, false)
		}
	})
}

func BenchmarkForwardTCP(b *testing.B) {
	back := tcpTarget(b, 0)
	cfg := testConfig()
	cfg.Groups[0].TCP = allocateTCP(b)
	cfg.Groups[0].Backends = []Backend{spec("a", back.Addr().String(), 1)}
	running(b, cfg)
	conn := connection(b, "tcp", cfg.Groups[0].TCP)
	_ = conn.SetDeadline(time.Time{})
	input, output := make([]byte, 32768), make([]byte, 32768)
	b.SetBytes(int64(len(input) + len(output)))
	b.ResetTimer()
	for range b.N {
		if _, err := conn.Write(input); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(conn, output); err != nil {
			b.Fatal(err)
		}
	}
}
