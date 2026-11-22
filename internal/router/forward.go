package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

var tcpBuffers = sync.Pool{New: func() any { return new([32 * 1024]byte) }}
var udpBuffers = sync.Pool{New: func() any { return new([65536]byte) }}

type tcpActivity struct {
	last     atomic.Int64
	idle     time.Duration
	upstream net.Conn
}

type meteredWriter struct {
	dst          io.Writer
	last         *atomic.Int64
	group, total *atomic.Uint64
}

func (writer meteredWriter) Write(data []byte) (int, error) {
	count, err := writer.dst.Write(data)
	writer.record(count)
	return count, err
}

func (writer meteredWriter) record(count int) {
	if count > 0 {
		writer.last.Store(time.Now().UnixNano())
		writer.group.Add(uint64(count))
		writer.total.Add(uint64(count))
	}
}

func copyTCPBuffered(src *net.TCPConn, writer meteredWriter) error {
	buffer := tcpBuffers.Get().(*[32 * 1024]byte)
	defer tcpBuffers.Put(buffer)
	_, err := io.CopyBuffer(writer, struct{ io.Reader }{src}, buffer[:])
	return err
}

func (e *Engine) acceptTCP(ctx context.Context, listener net.Listener, group string, wg *sync.WaitGroup) error {
	var retryDelay time.Duration
	for {
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			var networkError net.Error
			if !errors.As(err, &networkError) || !networkError.Temporary() {
				return err
			}
			retryDelay = min(time.Second, max(5*time.Millisecond, 2*retryDelay))
			slog.Warn("retrying TCP accept", "group", group, "delay", retryDelay, "error", err)
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		retryDelay = 0
		if !e.admit() {
			e.rejected.Add(1)
			conn.Close()
			continue
		}
		wg.Go(func() {
			defer conn.Close()
			defer e.unadmit()
			e.forwardTCP(ctx, conn.(*net.TCPConn), group)
		})
	}
}

func (e *Engine) forwardTCP(ctx context.Context, client *net.TCPConn, group string) {
	res := e.choose(group, false, nil)
	var upstream net.Conn
	retried := false
	if res.b != nil {
		dialer := net.Dialer{Timeout: res.dial, KeepAlive: 30 * time.Second}
		upstream, _ = dialer.DialContext(ctx, "tcp", res.target)
		if upstream == nil {
			e.release(res.b, false)
			res = e.choose(group, false, res.b)
			if res.b != nil {
				dialer.Timeout = res.dial
				upstream, _ = dialer.DialContext(ctx, "tcp", res.target)
				if upstream == nil {
					e.release(res.b, false)
				}
			}
			retried = true
		}
	}
	if upstream == nil {
		e.rejected.Add(1)
		return
	}
	defer upstream.Close()
	defer e.release(res.b, false)
	e.selected(res, "tcp", retried)
	peer := upstream.(*net.TCPConn)
	activity := &tcpActivity{idle: res.idle, upstream: peer}
	activity.last.Store(time.Now().UnixNano())
	e.flows.Store(client, activity)
	defer e.flows.Delete(client)
	var wg sync.WaitGroup
	copyHalf := func(dst, src *net.TCPConn, backend, total *atomic.Uint64) {
		writer := meteredWriter{dst: dst, last: &activity.last, group: backend, total: total}
		if err := copyTCP(dst, src, writer); err != nil {
			dst.Close()
			src.Close()
		}
		_ = dst.CloseWrite()
	}
	wg.Go(func() { copyHalf(peer, client, &res.b.rx, &e.rx) })
	copyHalf(client, peer, &res.b.tx, &e.tx)
	wg.Wait()
}

type udpFlow struct {
	conn *net.UDPConn
	res  reservation
	last atomic.Int64
}

func (e *Engine) serveUDP(ctx context.Context, listener *net.UDPConn, group string) error {
	buffer := udpBuffers.Get().(*[65536]byte)
	defer udpBuffers.Put(buffer)
	flows := make(map[netip.AddrPort]*udpFlow)
	var mu sync.Mutex
	var workers sync.WaitGroup
	defer func() {
		mu.Lock()
		for _, flow := range flows {
			flow.conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	}()
	for {
		size, client, err := listener.ReadFromUDPAddrPort(buffer[:])
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		mu.Lock()
		flow := flows[client]
		if flow == nil {
			res := e.choose(group, true, nil)
			if res.b == nil {
				mu.Unlock()
				e.rejected.Add(1)
				continue
			}
			upstream, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.MustParseAddrPort(res.target)))
			if err != nil {
				e.release(res.b, true)
				mu.Unlock()
				e.rejected.Add(1)
				continue
			}
			flow = &udpFlow{conn: upstream, res: res}
			flow.last.Store(time.Now().UnixNano())
			flows[client] = flow
			e.selected(res, "udp", false)
			workers.Go(func() {
				e.receiveUDP(listener, client, flow)
				mu.Lock()
				delete(flows, client)
				mu.Unlock()
				flow.conn.Close()
				e.release(res.b, true)
			})
		}
		mu.Unlock()
		flow.last.Store(time.Now().UnixNano())
		_ = flow.conn.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
		written, err := flow.conn.Write(buffer[:size])
		if err == nil {
			flow.res.b.rx.Add(uint64(written))
			e.rx.Add(uint64(written))
		}
	}
}

func (e *Engine) receiveUDP(listener *net.UDPConn, client netip.AddrPort, flow *udpFlow) {
	buffer := udpBuffers.Get().(*[65536]byte)
	defer udpBuffers.Put(buffer)
	for {
		deadline := time.Unix(0, flow.last.Load()).Add(flow.res.idle)
		_ = flow.conn.SetReadDeadline(deadline)
		size, err := flow.conn.Read(buffer[:])
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() &&
				time.Since(time.Unix(0, flow.last.Load())) < flow.res.idle {
				continue
			}
			return
		}
		flow.last.Store(time.Now().UnixNano())
		if written, err := listener.WriteToUDPAddrPort(buffer[:size], client); err == nil {
			flow.res.b.tx.Add(uint64(written))
			e.tx.Add(uint64(written))
		}
	}
}
