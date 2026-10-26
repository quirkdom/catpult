// Package transport is a thin wrapper over the Tailcat Go library so the
// rest of catpult does not depend on Tailcat implementation details.
// One TCP port carries all Catpult traffic.
package transport

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
)

// Port carries all Catpult application traffic.
const Port uint16 = 8411

// DialTimeout bounds the receiver's initial connection establishment.
const DialTimeout = 30 * time.Second

// Relay selects the bootstrap relay a room uses, by mechanism: either
// self-hosted relay endpoints given directly (no DERP map is fetched),
// or a URL pointing at a JSON DERP map to select from. Its zero value
// means Tailcat's default: the default DERP map, nearest region
// auto-selected. The two custom mechanisms are mutually exclusive.
type Relay struct {
	// Endpoints, if non-empty, names self-hosted DERP relay endpoints
	// directly (the cmd/tailcat genkey --region comma-separated-hostnames
	// mechanism): one relay node per endpoint, DERP over TLS, port
	// defaulting to 443. Entries are host[:port] (or [v6host]:port).
	Endpoints []string

	// MapURL, if non-empty, is an alternate URL to fetch the JSON DERP
	// relay map from when picking the room's bootstrap relay. http and
	// https are both accepted, matching tailcat's --derpmap-url.
	MapURL string
}

// debugLogf surfaces Tailcat's relay logs when CATPULT_DEBUG is set.
func debugLogf() logger.Logf {
	if os.Getenv("CATPULT_DEBUG") == "" {
		return logger.Discard
	}
	return log.Printf
}

// IsKey reports whether s parses as a Tailcat invite address (the KEY
// form). Uses tailcat's own decoder, so receiver disambiguation matches
// what Dial will accept.
func IsKey(s string) bool {
	if !strings.HasPrefix(s, "tc") {
		return false
	}
	_, err := tailcat.ParseAddr(tailcat.Addr(s))
	return err == nil
}

// NewServer starts an ephemeral Tailcat server whose every inbound TCP
// connection on Port is handed to handler in its own goroutine.
// It returns the server and its invite address (to be shared out of band).
// relay selects the room's bootstrap relay; see the Relay type.
// In either custom-relay case the chosen relay's details are embedded in
// the returned invite, so recipients need no relay configuration.
func NewServer(handler func(net.Conn), relay Relay) (*tailcat.Server, tailcat.Addr, error) {
	if len(relay.Endpoints) > 0 && relay.MapURL != "" {
		return nil, "", fmt.Errorf("--derp and --derpmap are mutually exclusive")
	}
	s := &tailcat.Server{
		Logf:           debugLogf(),
		ServedTCPPorts: []filter.PortRange{{First: Port, Last: Port}},
		OnTCP: func(port uint16) func(net.Conn) {
			if port != Port {
				return nil
			}
			return handler
		},
	}
	if len(relay.Endpoints) > 0 {
		reg, err := relayRegion(relay.Endpoints)
		if err != nil {
			return nil, "", err
		}
		s.Region = reg
	} else if relay.MapURL != "" {
		s.DERPMapURL = relay.MapURL
	}
	if err := s.Start(); err != nil {
		return nil, "", fmt.Errorf("starting tailcat server: %w", err)
	}
	return s, s.TailcatAddr(), nil
}

// relayRegion builds the synthetic DERP region that Server.Region expects
// for self-hosted relay endpoints (the same shape cmd/tailcat's genkey
// --region uses for its comma-separated hostnames): one relay node per
// endpoint. Nodes use DERP over TLS (DERPPort 0 meaning the default 443)
// with STUN disabled (no doomed 3478 probes; direct paths still come from
// the tunnel's own endpoint exchange) and IPv6 probes off, mirroring
// Tailcat's own local relay-region setup.
func relayRegion(endpoints []string) (*tailcfg.DERPRegion, error) {
	reg := &tailcfg.DERPRegion{RegionID: 1, RegionCode: "catpult", RegionName: "catpult"}
	for i, ep := range endpoints {
		host, port, err := relayEndpointHostPort(ep)
		if err != nil {
			return nil, err
		}
		reg.Nodes = append(reg.Nodes, &tailcfg.DERPNode{
			Name:     fmt.Sprintf("c%d", i+1),
			RegionID: reg.RegionID,
			HostName: host,
			IPv6:     "none",
			STUNPort: -1,
			DERPPort: port, // 0 = default 443
		})
	}
	return reg, nil
}

// ParseRelayEndpoints splits a comma-separated relay endpoint list and
// validates each entry, so malformed --derp values surface as flag
// errors rather than relay-region errors deep inside Tailcat.
func ParseRelayEndpoints(spec string) ([]string, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("empty; expected comma-separated host[:port] relay endpoint(s)")
	}
	var ends []string
	for _, s := range strings.Split(spec, ",") {
		s = strings.TrimSpace(s)
		if _, _, err := relayEndpointHostPort(s); err != nil {
			return nil, err
		}
		ends = append(ends, s)
	}
	return ends, nil
}

// relayEndpointHostPort parses and validates one relay endpoint:
// host[:port] (or [v6host]:port for IPv6 literals). An absent port means
// 0, which DERPPort semantics turn into the default 443.
func relayEndpointHostPort(ep string) (host string, port int, err error) {
	// Reject anything that looks like a URL up front (a scheme is
	// --derpmap's job) before parsing reports confusingly about it.
	if i := strings.Index(ep, "://"); i >= 0 {
		return "", 0, fmt.Errorf("relay endpoint %q: URLs are not accepted; give a bare host[:port], or a DERP map URL to --derpmap", ep)
	}
	if ep == "" {
		return "", 0, fmt.Errorf("relay endpoint %q: empty", ep)
	}
	host = ep
	if h, p, perr := net.SplitHostPort(ep); perr == nil {
		host = h
		if p != "" {
			n, nerr := strconv.Atoi(p)
			if nerr != nil || n <= 0 || n > 65535 {
				return "", 0, fmt.Errorf("relay endpoint %q: bad port %q", ep, p)
			}
			port = n
		}
	}
	host, ok := stripBrackets(host)
	if !ok {
		return "", 0, fmt.Errorf("relay endpoint %q: unbalanced brackets", ep)
	}
	if host == "" {
		return "", 0, fmt.Errorf("relay endpoint %q: missing host", ep)
	}
	if net.ParseIP(host) == nil && !isDNSName(host) {
		return "", 0, fmt.Errorf("relay endpoint %q: invalid host %q", ep, host)
	}
	return host, port, nil
}

// stripBrackets strips IPv6 literal brackets, reporting whether s has a
// well-formed ("[...]") or absent bracket structure.
func stripBrackets(s string) (string, bool) {
	if !strings.HasSuffix(s, "]") {
		return s, !strings.Contains(s, "]")
	}
	if !strings.HasPrefix(s, "[") {
		return "", false
	}
	return s[1 : len(s)-1], true
}

// isDNSName reports whether s is a conservative ASCII DNS name (letters,
// digits, dots, hyphens) — the shape a self-hosted relay endpoint is
// given by hand in cmd/tailcat's --region.
func isDNSName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// Dial connects to the room host identified by key.
func Dial(ctx context.Context, key string) (net.Conn, *tailcat.Client, error) {
	cl := tailcat.NewClient(tailcat.Addr(key))
	cl.Logf = debugLogf()
	conn, err := cl.DialTCPPort(ctx, Port)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fmt.Errorf("connecting to room: %w", err)
	}
	return conn, cl, nil
}
