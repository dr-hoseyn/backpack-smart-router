package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
)

func TestKernelTrafficMergesFreshCounters(t *testing.T) {
	const addr = "192.0.2.2:443"
	path := filepath.Join(t.TempDir(), "traffic.json")
	now := time.Now().Truncate(time.Second)
	write := func(at time.Time, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	var sampler kernelSampler
	write(now.Add(-2*time.Second), `{"backends":{"192.0.2.2:443":{"rx":1000,"tx":2000}}}`)
	if sampler.update(path, now) {
		t.Fatal("first sample has no rate")
	}
	write(now, `{"backends":{"192.0.2.2:443":{"rx":1600,"tx":2600}}}`)
	if !sampler.update(path, now) || !sampler.update(path, now.Add(time.Second)) {
		t.Fatal("fresh rate should survive repeated reads of the same sample")
	}
	status := router.Status{RX: 10, TX: 20, RXRate: 4, TXRate: 5, Active: 12,
		Backends: []router.BackendStatus{{Backend: router.Backend{TCP: addr}, RX: 10, TX: 20, RXRate: 4, TXRate: 5, Active: 12}}}
	sampler.addTraffic(&status)
	backend := status.Backends[0]
	if status.RX != 1610 || status.TX != 2620 || status.RXRate != 304 || status.TXRate != 305 || status.Active != 12 {
		t.Fatalf("incorrect totals: %+v", status)
	}
	if backend.RX != status.RX || backend.TX != status.TX || backend.RXRate != status.RXRate || backend.TXRate != status.TXRate || backend.Active != 12 {
		t.Fatalf("incorrect per-backend traffic: %+v", backend)
	}
	if sampler.update(path, now.Add(9*time.Second)) {
		t.Fatal("stale sample should be unavailable")
	}
	write(now.Add(2*time.Second), `{"group":"vless","backends":{"192.0.2.2:443":{"rx":1700,"tx":2800,"active":9}}}`)
	if !sampler.update(path, now.Add(2*time.Second)) {
		t.Fatal("updated kernel sample should be available")
	}
	status = router.Status{Active: 2, Backends: []router.BackendStatus{
		{Backend: router.Backend{TCP: addr}, Group: "vless", Active: 2, TCPActive: 2},
		{Backend: router.Backend{TCP: addr}, Group: "other"},
	}}
	if !sampler.addTraffic(&status) || status.Active != 11 || status.Backends[0].TCPActive != 11 ||
		status.Backends[1].Active != 0 || status.Backends[1].RX != 0 {
		t.Fatalf("kernel connections or traffic attributed to the wrong group: %+v", status)
	}
}
