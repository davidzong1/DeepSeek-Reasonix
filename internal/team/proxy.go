package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// DefaultProxyAddress is the proxy fallback when an enabled config names none.
const DefaultProxyAddress = "127.0.0.1:7980"

// ErrInvalidProxy reports a proxy config the store refuses: an enabled proxy
// must name a resolvable IP:port address, host part a literal IP (no DNS).
var ErrInvalidProxy = errors.New("team: invalid proxy config: enabled proxy needs an IP:port address like 127.0.0.1:7980")

// UnmarshalJSON reads both document generations: the current address form and
// the legacy host/port split written before the address change. A legacy doc
// assembles host:port into Address so old registries load unchanged and the
// next write migrates them to the single field.
func (p *ProxyConfig) UnmarshalJSON(b []byte) error {
	var legacy struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil {
		return err
	}
	p.Enabled = legacy.Enabled
	p.Address = legacy.Address
	if p.Address == "" && legacy.Host != "" {
		p.Address = net.JoinHostPort(legacy.Host, strconv.Itoa(legacy.Port))
	}
	return nil
}

// SetTeamProxy sets the team-default proxy; an enabled config without a
// resolvable IP:port address is refused. An enabled config naming no address
// is normalized to the default (127.0.0.1:7980) before it lands on disk, so
// the stored shape is always explicit. The team default stays explicit: pass
// ProxyConfig{} to record "off".
func (s *TeamStore) SetTeamProxy(teamName string, p ProxyConfig) error {
	if p.Enabled && p.Address == "" {
		p.Address = DefaultProxyAddress
	}
	if err := validateProxy(p); err != nil {
		return err
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		doc.Teams[i].Proxy = &p
		return nil
	})
}

// SetMemberProxyOverride sets a member's proxy override (§4.3): nil inherits
// the team default, true forces the proxy on, false forces it off. The
// override is stored as a *bool; nil is how the caller expresses inheritance.
func (s *TeamStore) SetMemberProxyOverride(teamName, memberID string, enabled *bool) error {
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		slot.ProxyEnabled = enabled
		return nil
	})
}

// ProxyFor resolves the effective proxy for a member (§4.3): the member
// override (nil = inherit, true = force on, false = force off) beats the team
// default, which beats off. The bool reports whether the proxy is on; a
// force-on without a team default resolves on with the default address, so
// the caller still sees the member's intent.
func ProxyFor(team *ProxyConfig, memberOverride *bool) (ProxyConfig, bool) {
	if memberOverride != nil {
		if *memberOverride {
			return copyProxy(team), true
		}
		return ProxyConfig{}, false
	}
	if team != nil && team.Enabled {
		return withDefaultProxy(*team), true
	}
	return ProxyConfig{}, false
}

// validateProxy refuses an enabled proxy whose address is not a resolvable
// IP:port pair: a literal IP host (DNS names are not allowed), and a port in
// 1-65535. An empty address with enabled is legal — the default applies.
func validateProxy(p ProxyConfig) error {
	if !p.Enabled {
		return nil
	}
	addr := p.Address
	if addr == "" {
		addr = DefaultProxyAddress
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProxy, err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("%w: host %q is not a literal IP", ErrInvalidProxy, host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: port %q outside 1-65535", ErrInvalidProxy, port)
	}
	return nil
}

// withDefaultProxy returns the config with the default address filled in, so
// a stored config that names no address still resolves to one.
func withDefaultProxy(p ProxyConfig) ProxyConfig {
	if p.Address == "" {
		p.Address = DefaultProxyAddress
	}
	return p
}

// copyProxy returns the team default under a force-on override; nil team
// defaults copy as a bare on with the default address, so the config's
// Enabled always matches the resolved flag.
func copyProxy(team *ProxyConfig) ProxyConfig {
	if team == nil {
		return ProxyConfig{Enabled: true, Address: DefaultProxyAddress}
	}
	p := *team
	p.Enabled = true
	return withDefaultProxy(p)
}
