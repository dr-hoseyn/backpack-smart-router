package panel

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/dr-hoseyn/backpack-smart-router/internal/router"
)

type kernelBytes struct {
	RX     uint64 `json:"rx"`
	TX     uint64 `json:"tx"`
	Active *int   `json:"active,omitempty"`
}

type kernelSample struct {
	at       time.Time
	Group    string                 `json:"group,omitempty"`
	Backends map[string]kernelBytes `json:"backends"`
}

type kernelSampler struct {
	previous, current kernelSample
}

func (s *kernelSampler) update(path string, now time.Time) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || now.Sub(info.ModTime()) > 8*time.Second {
		return false
	}
	if !info.ModTime().Equal(s.current.at) {
		next := kernelSample{at: info.ModTime()}
		if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&next); err != nil || len(next.Backends) == 0 {
			return false
		}
		s.previous, s.current = s.current, next
	}
	return s.current.at.After(s.previous.at) && !s.previous.at.IsZero()
}

func (s kernelSampler) addTraffic(status *router.Status) bool {
	type rates struct{ rx, tx float64 }
	perBackend := make(map[string]rates, len(s.current.Backends))
	seconds := s.current.at.Sub(s.previous.at).Seconds()
	counted := true
	for addr, counters := range s.current.Backends {
		r := rates{}
		if previous, ok := s.previous.Backends[addr]; ok && counters.RX >= previous.RX && counters.TX >= previous.TX {
			r = rates{rx: float64(counters.RX-previous.RX) / seconds, tx: float64(counters.TX-previous.TX) / seconds}
		}
		perBackend[addr] = r
		status.RX += counters.RX
		status.TX += counters.TX
		status.RXRate += r.rx
		status.TXRate += r.tx
		if counters.Active != nil && *counters.Active >= 0 {
			status.Active += *counters.Active
		} else {
			counted = false
		}
	}
	for i := range status.Backends {
		backend := &status.Backends[i]
		if s.current.Group != "" && backend.Group != s.current.Group {
			continue
		}
		counters := s.current.Backends[backend.TCP]
		r := perBackend[backend.TCP]
		backend.RX += counters.RX
		backend.TX += counters.TX
		backend.RXRate += r.rx
		backend.TXRate += r.tx
		if counters.Active != nil && *counters.Active >= 0 {
			backend.Active += *counters.Active
			backend.TCPActive += *counters.Active
		}
	}
	return counted
}
