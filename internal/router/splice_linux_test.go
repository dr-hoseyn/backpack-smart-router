package router

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func spliceTestPair(t testing.TB) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func TestSpliceReportsLiveTraffic(t *testing.T) {
	client, input := spliceTestPair(t)
	output, target := spliceTestPair(t)
	t.Cleanup(func() { _ = input.Close(); _ = output.Close() })
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_ = target.SetDeadline(time.Now().Add(3 * time.Second))

	var last atomic.Int64
	var route, total atomic.Uint64
	type outcome struct {
		handled bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		handled, err := spliceTCP(output, input, meteredWriter{dst: output, last: &last, group: &route, total: &total})
		done <- outcome{handled, err}
		_ = output.CloseWrite()
	}()

	payload := bytes.Repeat([]byte("router stream"), 8192)
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(target, received); err != nil || !bytes.Equal(received, payload) {
		t.Fatalf("payload over splice differed: %v", err)
	}
	waitFor(t, func() bool { return total.Load() == uint64(len(payload)) })
	if route.Load() != total.Load() || last.Load() == 0 {
		t.Fatal("route counters or activity were not updated while the stream was open")
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Read(received[:1]); err != io.EOF {
		t.Fatalf("expected destination EOF after half-close, got %v", err)
	}
	if result := <-done; !result.handled || result.err != nil {
		t.Fatalf("splice did not complete: %+v", result)
	}
}

func BenchmarkTCPCopyModes(b *testing.B) {
	type copier func(*net.TCPConn, *net.TCPConn, meteredWriter) error
	modes := []struct {
		name string
		copy copier
	}{
		{"buffered", func(_ *net.TCPConn, src *net.TCPConn, meter meteredWriter) error { return copyTCPBuffered(src, meter) }},
		{"splice", func(dst, src *net.TCPConn, meter meteredWriter) error {
			if handled, err := spliceTCP(dst, src, meter); handled {
				return err
			}
			return copyTCPBuffered(src, meter)
		}},
		{"adaptive", copyTCP},
	}
	for _, size := range []int{512, 4096, 32 * 1024} {
		for _, mode := range modes {
			b.Run(fmt.Sprintf("%d/%s", size, mode.name), func(b *testing.B) {
				client, input := spliceTestPair(b)
				output, target := spliceTestPair(b)
				var last atomic.Int64
				var route, total atomic.Uint64
				var workers sync.WaitGroup
				pump := func(dst, src *net.TCPConn) {
					defer dst.CloseWrite()
					if err := mode.copy(dst, src, meteredWriter{dst: dst, last: &last, group: &route, total: &total}); err != nil {
						_ = src.Close()
						_ = dst.Close()
					}
				}
				workers.Go(func() { pump(output, input) })
				workers.Go(func() { pump(input, output) })
				workers.Go(func() { _, _ = io.Copy(target, target); _ = target.CloseWrite() })
				defer func() {
					_ = client.Close()
					_ = target.Close()
					_ = input.Close()
					_ = output.Close()
					workers.Wait()
				}()
				payload, received := make([]byte, size), make([]byte, size)
				b.SetBytes(int64(size * 2))
				var start, end unix.Rusage
				if err := unix.Getrusage(unix.RUSAGE_SELF, &start); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for range b.N {
					if _, err := client.Write(payload); err != nil {
						b.Fatal(err)
					}
					if _, err := io.ReadFull(client, received); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if err := unix.Getrusage(unix.RUSAGE_SELF, &end); err != nil {
					b.Fatal(err)
				}
				duration := end.Utime.Nano() + end.Stime.Nano() - start.Utime.Nano() - start.Stime.Nano()
				b.ReportMetric(float64(duration)/float64(b.N), "cpu-ns/op")
			})
		}
	}
}
