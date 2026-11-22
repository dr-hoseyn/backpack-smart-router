package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

type Backend struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TCP      string `json:"tcp"`
	UDP      string `json:"udp"`
	Probe    string `json:"probe"`
	Weight   int    `json:"weight"`
	Capacity int    `json:"capacity"`
	RateMbps int    `json:"rateMbps"`
	Enabled  bool   `json:"enabled"`
	Drain    bool   `json:"drain"`
}

type Group struct {
	ID       string    `json:"id"`
	TCP      string    `json:"tcp"`
	UDP      string    `json:"udp"`
	Backends []Backend `json:"backends"`
}

type Config struct {
	Revision        uint64             `json:"revision"`
	Strategy        string             `json:"strategy"`
	MaxFlows        int                `json:"maxFlows"`
	DialTimeoutMS   int                `json:"dialTimeoutMS"`
	TCPIdleSeconds  int                `json:"tcpIdleSeconds"`
	UDPIdleSeconds  int                `json:"udpIdleSeconds"`
	CheckIntervalMS int                `json:"checkIntervalMS"`
	CheckTimeoutMS  int                `json:"checkTimeoutMS"`
	Failures        int                `json:"failures"`
	Recoveries      int                `json:"recoveries"`
	CooldownSeconds int                `json:"cooldownSeconds"`
	Weights         map[string]float64 `json:"weights"`
	Groups          []Group            `json:"groups"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

func endpoint(value string, private bool) bool {
	address, err := netip.ParseAddrPort(value)
	if err != nil || address.Port() == 0 || address.Addr().Zone() != "" {
		return false
	}
	ip := address.Addr().Unmap()
	if ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	return !private || ip.IsPrivate() || ip.IsLoopback()
}

func (c Config) Validate() error {
	if !slices.Contains([]string{"weighted-least", "weighted-round-robin", "least-load", "least-rtt", "adaptive"}, c.Strategy) {
		return errors.New("unknown strategy")
	}
	if c.MaxFlows < 1 || c.MaxFlows > 200000 || c.DialTimeoutMS < 50 || c.DialTimeoutMS > 30000 ||
		c.TCPIdleSeconds < 1 || c.TCPIdleSeconds > 86400 || c.UDPIdleSeconds < 1 || c.UDPIdleSeconds > 3600 ||
		c.CheckIntervalMS < 500 || c.CheckIntervalMS > 300000 || c.CheckTimeoutMS < 100 ||
		c.CheckTimeoutMS > 30000 || c.Failures < 1 || c.Failures > 20 || c.Recoveries < 1 || c.Recoveries > 20 ||
		c.CooldownSeconds < 0 || c.CooldownSeconds > 3600 {
		return errors.New("invalid flow or health limits")
	}
	for key, value := range c.Weights {
		if !slices.Contains([]string{"flows", "rate", "rtt", "cpu", "ram", "retransmits", "loss"}, key) ||
			math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return fmt.Errorf("invalid weight for %q", key)
		}
	}
	if len(c.Groups) == 0 || len(c.Groups) > 16 {
		return errors.New("expected 1–16 groups")
	}
	ids, listeners := map[string]bool{}, map[string]bool{}
	for _, group := range c.Groups {
		if !identifier.MatchString(group.ID) || ids[group.ID] || (group.TCP == "" && group.UDP == "") {
			return fmt.Errorf("invalid group %q", group.ID)
		}
		ids[group.ID] = true
		for protocol, target := range map[string]string{"tcp": group.TCP, "udp": group.UDP} {
			if target == "" {
				continue
			}
			if !endpoint(target, false) {
				return fmt.Errorf("invalid %s listener for %q", protocol, group.ID)
			}
			key := protocol + netip.MustParseAddrPort(target).String()
			if listeners[key] {
				return fmt.Errorf("invalid or duplicate %s listener for %q", protocol, group.ID)
			}
			listeners[key] = true
		}
		if len(group.Backends) > 32 {
			return fmt.Errorf("too many backends for %q", group.ID)
		}
	}
	for _, group := range c.Groups {
		for _, b := range group.Backends {
			if !identifier.MatchString(b.ID) || ids[b.ID] || len(b.Name) == 0 || len(b.Name) > 120 ||
				b.Weight < 1 || b.Weight > 1000 || b.Capacity < 1 || b.Capacity > 200000 ||
				b.RateMbps < 0 || b.RateMbps > 1000000 || (b.TCP == "" && b.UDP == "") ||
				(b.TCP == "" && b.Probe == "") {
				return fmt.Errorf("invalid backend %q", b.ID)
			}
			ids[b.ID] = true
			for protocol, target := range map[string]string{"tcp": b.TCP, "udp": b.UDP, "probe": b.Probe} {
				if target == "" {
					continue
				}
				if !endpoint(target, true) {
					return fmt.Errorf("expected private IP and port for %s on %q", protocol, b.ID)
				}
				if protocol == "probe" {
					destination := b.TCP
					if destination == "" {
						destination = b.UDP
					}
					if netip.MustParseAddrPort(target).Addr().Unmap() != netip.MustParseAddrPort(destination).Addr().Unmap() {
						return fmt.Errorf("probe for %q must run on the backend VM", b.ID)
					}
				}
				addr := netip.MustParseAddrPort(target)
				wire := protocol
				if wire == "probe" {
					wire = "tcp"
				}
				if listeners[wire+addr.String()] {
					return fmt.Errorf("backend %q uses a frontend port", b.ID)
				}
			}
		}
	}
	return nil
}

func Decode(reader io.Reader) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("expected one configuration object")
	}
	return cfg, cfg.Validate()
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Decode(bytes.NewReader(data))
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".router-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := file.Chmod(0640); err != nil {
		return err
	}
	if err := encoder.Encode(cfg); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err == nil {
		defer directory.Close()
		// Some filesystems do not support syncing directory handles.
		_ = directory.Sync()
	}
	return nil
}

func SameListeners(a, b Config) bool {
	if len(a.Groups) != len(b.Groups) {
		return false
	}
	for _, g := range a.Groups {
		if !slices.ContainsFunc(b.Groups, func(other Group) bool {
			return g.ID == other.ID && g.TCP == other.TCP && g.UDP == other.UDP
		}) {
			return false
		}
	}
	return true
}
