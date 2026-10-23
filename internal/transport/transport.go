// Package transport is a thin wrapper over the Tailcat Go library so the
// rest of catpult does not depend on Tailcat implementation details.
// One TCP port carries all Catpult traffic.
package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
)

// Port carries all Catpult application traffic.
const Port uint16 = 8411

// DialTimeout bounds the receiver's initial connection establishment.
const DialTimeout = 30 * time.Second

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
func NewServer(handler func(net.Conn)) (*tailcat.Server, tailcat.Addr, error) {
	s := &tailcat.Server{
		Logf:           logger.Discard,
		ServedTCPPorts: []filter.PortRange{{First: Port, Last: Port}},
		OnTCP: func(port uint16) func(net.Conn) {
			if port != Port {
				return nil
			}
			return handler
		},
	}
	if err := s.Start(); err != nil {
		return nil, "", fmt.Errorf("starting tailcat server: %w", err)
	}
	return s, s.TailcatAddr(), nil
}

// Dial connects to the room host identified by key.
func Dial(ctx context.Context, key string) (net.Conn, *tailcat.Client, error) {
	cl := tailcat.NewClient(tailcat.Addr(key))
	cl.Logf = logger.Discard
	conn, err := cl.DialTCPPort(ctx, Port)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fmt.Errorf("connecting to room: %w", err)
	}
	return conn, cl, nil
}
