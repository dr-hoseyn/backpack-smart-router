package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type Telemetry struct {
	CPU         *float64 `json:"cpu"`
	RAM         *float64 `json:"ram"`
	Loss        *float64 `json:"loss"`
	Retransmits *float64 `json:"retransmits"`
	ProbeMS     *float64 `json:"probeMS"`
	InterfaceRX *uint64  `json:"interfaceRx"`
	InterfaceTX *uint64  `json:"interfaceTx"`
	Verified    bool     `json:"verified"`
}

type BackendStatus struct {
	Backend
	Group       string    `json:"group"`
	State       string    `json:"state"`
	HealthScore int       `json:"healthScore"`
	Active      int       `json:"active"`
	TCPActive   int       `json:"tcpActive"`
	UDPActive   int       `json:"udpActive"`
	Selections  uint64    `json:"selections"`
	RX          uint64    `json:"rx"`
	TX          uint64    `json:"tx"`
	RXRate      float64   `json:"rxRate"`
	TXRate      float64   `json:"txRate"`
	RTT         float64   `json:"rtt"`
	LastOK      time.Time `json:"lastOK"`
	Failure     string    `json:"failure"`
	KernelTLS   *bool     `json:"kernelTLS,omitempty"`
	Telemetry
}

type Event struct {
	ID      uint64    `json:"id"`
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Backend string    `json:"backend,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

type Decision struct {
	Time     time.Time `json:"time"`
	Group    string    `json:"group"`
	Backend  string    `json:"backend"`
	Protocol string    `json:"protocol"`
	Strategy string    `json:"strategy"`
	Score    float64   `json:"score"`
	Retry    bool      `json:"retry"`
}

type Status struct {
	Started  time.Time       `json:"started"`
	Revision uint64          `json:"revision"`
	Strategy string          `json:"strategy"`
	Backends []BackendStatus `json:"backends"`
	Events   []Event         `json:"events"`
	Decision *Decision       `json:"decision"`
	Active   int             `json:"active"`
	Rejected uint64          `json:"rejected"`
	RX       uint64          `json:"rx"`
	TX       uint64          `json:"tx"`
	RXRate   float64         `json:"rxRate"`
	TXRate   float64         `json:"txRate"`
}

type backend struct {
	spec                 Backend
	group                string
	health               string
	failure              string
	lastOK               time.Time
	recoverAfter         time.Time
	successes, failures  int
	telemetry            Telemetry
	rtt, rxRate, txRate  float64
	rxLast, txLast       uint64
	rx, tx               atomic.Uint64
	tcpActive, udpActive int
	count                uint64
	rr                   [2]int
	probing              bool
}

type Engine struct {
	mu               sync.Mutex
	cfg              Config
	byGroup          map[string][]*backend
	start            time.Time
	events           [256]Event
	seq              uint64
	decision         *Decision
	active, admitted int
	rejected         atomic.Uint64
	rx, tx           atomic.Uint64
	rxRate, txRate   float64
	lastRetryEvent   time.Time
	flows            sync.Map
}

func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.ValidateLocal(); err != nil {
		return nil, err
	}
	e := &Engine{start: time.Now()}
	e.install(cfg)
	e.record("started", "", "router engine started")
	return e, nil
}

func (e *Engine) install(cfg Config) {
	previous := map[string]*backend{}
	for _, backends := range e.byGroup {
		for _, b := range backends {
			previous[b.spec.ID] = b
		}
	}
	e.byGroup = make(map[string][]*backend, len(cfg.Groups))
	for _, group := range cfg.Groups {
		for _, spec := range group.Backends {
			b := previous[spec.ID]
			if b == nil || b.group != group.ID || b.spec.TCP != spec.TCP || b.spec.UDP != spec.UDP || b.spec.Probe != spec.Probe {
				b = &backend{group: group.ID, health: "checking"}
			} else if spec.Enabled != b.spec.Enabled {
				b.health, b.failure, b.successes, b.failures = "checking", "", 0, 0
				b.recoverAfter = time.Time{}
			}
			b.spec = spec
			e.byGroup[group.ID] = append(e.byGroup[group.ID], b)
		}
	}
	e.cfg = cfg
}

func (e *Engine) Apply(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.ValidateLocal(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !SameListeners(e.cfg, cfg) {
		return errors.New("listener changes require engine restart")
	}
	if cfg.Revision <= e.cfg.Revision {
		return errors.New("stale configuration revision")
	}
	e.install(cfg)
	e.record("configuration", "", fmt.Sprintf("revision %d", cfg.Revision))
	return nil
}

func (e *Engine) record(kind, id, detail string) {
	e.seq++
	event := Event{ID: e.seq, Time: time.Now().UTC(), Kind: kind, Backend: id, Detail: detail}
	e.events[(e.seq-1)%uint64(len(e.events))] = event
	slog.Info(kind, "backend", id, "detail", detail)
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := Status{
		Started: e.start, Revision: e.cfg.Revision, Strategy: e.cfg.Strategy, Active: e.active,
		Rejected: e.rejected.Load(), RX: e.rx.Load(), TX: e.tx.Load(), RXRate: e.rxRate, TXRate: e.txRate,
		Backends: []BackendStatus{}, Events: []Event{}, Decision: e.decision,
	}
	for _, group := range e.cfg.Groups {
		for _, b := range e.byGroup[group.ID] {
			state := b.health
			switch {
			case !b.spec.Enabled:
				state = "disabled"
			case b.spec.Drain && state == "healthy":
				state = "draining"
			}
			score := 0
			if b.health == "healthy" && b.spec.Enabled {
				score = 100 - 100*b.failures/e.cfg.Failures
			}
			status.Backends = append(status.Backends, BackendStatus{
				Backend: b.spec, Group: b.group, State: state, HealthScore: score, Active: b.tcpActive + b.udpActive,
				TCPActive: b.tcpActive, UDPActive: b.udpActive, Selections: b.count,
				RX: b.rx.Load(), TX: b.tx.Load(), RXRate: b.rxRate, TXRate: b.txRate,
				RTT: b.rtt, LastOK: b.lastOK, Failure: b.failure, Telemetry: b.telemetry,
			})
		}
	}
	start := uint64(0)
	if e.seq > uint64(len(e.events)) {
		start = e.seq - uint64(len(e.events))
	}
	for index := start; index < e.seq; index++ {
		status.Events = append(status.Events, e.events[index%uint64(len(e.events))])
	}
	return status
}

type reservation struct {
	b                *backend
	target, strategy string
	score            float64
	dial, idle       time.Duration
}

func (e *Engine) choose(group string, udp bool, excluded *backend) reservation {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.MaxFlows > 0 && e.active >= e.cfg.MaxFlows {
		return reservation{}
	}
	var chosen *backend
	var best float64
	var total int
	roundRobin := e.cfg.Strategy == "weighted-round-robin"
	index := 0
	if udp {
		index = 1
	}
	for _, b := range e.byGroup[group] {
		if b == excluded || !b.spec.Enabled || b.spec.Drain || b.health != "healthy" ||
			(udp && b.spec.UDP == "") || (!udp && b.spec.TCP == "") ||
			(b.spec.Capacity > 0 && b.tcpActive+b.udpActive >= b.spec.Capacity) {
			continue
		}
		var score float64
		if roundRobin {
			b.rr[index] += b.spec.Weight
			total += b.spec.Weight
			score = -float64(b.rr[index])
		} else {
			score = e.score(b)
		}
		if chosen == nil || score < best || (score == best && b.count < chosen.count) {
			chosen, best = b, score
		}
	}
	if chosen == nil {
		return reservation{}
	}
	chosen.rr[index] -= total
	chosen.count++
	e.active++
	res := reservation{b: chosen, target: chosen.spec.TCP, strategy: e.cfg.Strategy, score: best,
		dial: time.Duration(e.cfg.DialTimeoutMS) * time.Millisecond,
		idle: time.Duration(e.cfg.TCPIdleSeconds) * time.Second}
	if udp {
		chosen.udpActive++
		res.target, res.idle = chosen.spec.UDP, time.Duration(e.cfg.UDPIdleSeconds)*time.Second
	} else {
		chosen.tcpActive++
	}
	return res
}

func (e *Engine) score(b *backend) float64 {
	capacity := b.spec.Capacity
	if capacity == 0 {
		// Keep load comparable to the highest configured capacity without enforcing a limit.
		capacity = 200_000
	}
	load := float64(b.tcpActive+b.udpActive+1) / float64(capacity)
	switch e.cfg.Strategy {
	case "least-load":
		return load
	case "least-rtt":
		return max(1, b.rtt) * (1 + load)
	case "adaptive":
		sum, weights := 0.0, 0.0
		add := func(key string, value, defaultWeight float64) {
			weight, ok := e.cfg.Weights[key]
			if !ok {
				weight = defaultWeight
			}
			sum += weight * min(1, max(0, value))
			weights += weight
		}
		add("flows", load, 4)
		add("rtt", b.rtt/500, 2)
		if b.spec.RateMbps > 0 {
			add("rate", 8*(b.rxRate+b.txRate)/(1e6*float64(b.spec.RateMbps)), 3)
		}
		if b.telemetry.CPU != nil {
			add("cpu", *b.telemetry.CPU/100, 2)
		}
		if b.telemetry.RAM != nil {
			add("ram", *b.telemetry.RAM/100, 1)
		}
		if b.telemetry.Loss != nil {
			add("loss", *b.telemetry.Loss/100, 2)
		}
		if b.telemetry.Retransmits != nil {
			add("retransmits", *b.telemetry.Retransmits/100, 1)
		}
		if weights == 0 {
			return load
		}
		return (sum / weights) + 0.05*float64(b.failures)
	default:
		return float64(b.tcpActive+b.udpActive+1) / float64(b.spec.Weight)
	}
}

func (e *Engine) admit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.MaxFlows > 0 && e.admitted >= e.cfg.MaxFlows {
		return false
	}
	e.admitted++
	return true
}

func (e *Engine) unadmit() {
	e.mu.Lock()
	e.admitted--
	e.mu.Unlock()
}

func (e *Engine) release(b *backend, udp bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active--
	if udp {
		b.udpActive--
	} else {
		b.tcpActive--
	}
}

func (e *Engine) selected(res reservation, protocol string, retry bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.decision = &Decision{Time: time.Now(), Group: res.b.group, Backend: res.b.spec.ID, Protocol: protocol,
		Strategy: res.strategy, Score: res.score, Retry: retry}
	if retry && time.Since(e.lastRetryEvent) >= time.Second {
		e.lastRetryEvent = time.Now()
		e.record("dial-failover", res.b.spec.ID, "TCP connection retried before forwarding")
	}
}

func (cfg Config) ValidateLocal() error {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	local := make(map[netip.Addr]bool, len(addresses))
	for _, value := range addresses {
		if prefix, err := netip.ParsePrefix(value.String()); err == nil {
			local[prefix.Addr().Unmap()] = true
		}
	}
	listening := map[string]map[uint16]bool{"tcp": {}, "udp": {}}
	for _, group := range cfg.Groups {
		for protocol, address := range map[string]string{"tcp": group.TCP, "udp": group.UDP} {
			if address == "" {
				continue
			}
			endpoint := netip.MustParseAddrPort(address)
			if endpoint.Addr().IsUnspecified() {
				listening[protocol][endpoint.Port()] = true
			}
		}
	}
	for _, group := range cfg.Groups {
		for _, backend := range group.Backends {
			for protocol, address := range map[string]string{"tcp": backend.TCP, "udp": backend.UDP, "probe": backend.Probe} {
				if address == "" {
					continue
				}
				endpoint := netip.MustParseAddrPort(address)
				wire := protocol
				if wire == "probe" {
					wire = "tcp"
				}
				if listening[wire][endpoint.Port()] && (local[endpoint.Addr().Unmap()] || endpoint.Addr().IsLoopback()) {
					return fmt.Errorf("backend %q loops into a local wildcard listener", backend.ID)
				}
			}
		}
	}
	return nil
}

func (e *Engine) Serve(ctx context.Context) error {
	e.mu.Lock()
	groups := e.cfg.Groups
	e.mu.Unlock()
	var listeners []io.Closer
	var start []func()
	var workers sync.WaitGroup
	var sessions sync.WaitGroup
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, 1)
	fail := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	for _, g := range groups {
		if g.TCP != "" {
			listener, err := net.Listen("tcp", g.TCP)
			if err != nil {
				return fmt.Errorf("%s TCP: %w", g.ID, err)
			}
			listeners = append(listeners, listener)
			start = append(start, func() {
				if err := e.acceptTCP(ctx, listener, g.ID, &sessions); err != nil {
					fail(err)
				}
			})
		}
		if g.UDP != "" {
			listener, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort(g.UDP)))
			if err != nil {
				return fmt.Errorf("%s UDP: %w", g.ID, err)
			}
			listeners = append(listeners, listener)
			start = append(start, func() {
				if err := e.serveUDP(ctx, listener, g.ID); err != nil {
					fail(err)
				}
			})
		}
	}
	for _, run := range start {
		workers.Go(run)
	}
	workers.Go(func() { e.monitor(ctx) })
	workers.Go(func() { e.sample(ctx) })
	var cause error
	select {
	case <-ctx.Done():
	case cause = <-failures:
	}
	cancel()
	for _, listener := range listeners {
		listener.Close()
	}
	workers.Wait()
	finished := make(chan struct{})
	go func() { sessions.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		e.flows.Range(func(key, _ any) bool { key.(net.Conn).Close(); return true })
		<-finished
	}
	return cause
}

func (e *Engine) sample(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last, prevRX, prevTX := time.Now(), uint64(0), uint64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			seconds := now.Sub(last).Seconds()
			e.mu.Lock()
			rx, tx := e.rx.Load(), e.tx.Load()
			e.rxRate, e.txRate = float64(rx-prevRX)/seconds, float64(tx-prevTX)/seconds
			prevRX, prevTX, last = rx, tx, now
			for _, list := range e.byGroup {
				for _, b := range list {
					rx, tx := b.rx.Load(), b.tx.Load()
					b.rxRate = 0.35*float64(rx-b.rxLast)/seconds + 0.65*b.rxRate
					b.txRate = 0.35*float64(tx-b.txLast)/seconds + 0.65*b.txRate
					b.rxLast, b.txLast = rx, tx
				}
			}
			idleNow := now.UnixNano()
			e.mu.Unlock()
			e.flows.Range(func(key, value any) bool {
				flow := value.(*tcpActivity)
				if idleNow-flow.last.Load() > int64(flow.idle) {
					key.(net.Conn).Close()
					flow.upstream.Close()
				}
				return true
			})
		}
	}
}
