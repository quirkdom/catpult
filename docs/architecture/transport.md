# Tailcat transport boundary

`internal/transport` is intentionally a thin adapter. Catpult uses Tailcat's Go library directly; it never shells out to the `tailcat` executable and does not implement its own tunnel, NAT traversal, relay, or cryptography.

## Server and client

`NewServer` configures a `tailcat.Server` with one served TCP port (`8411`). Its `OnTCP` callback returns Catpult's connection handler for that port. The handler receives an ordinary `net.Conn`; all application framing and transfer logic lives above this boundary. `TailcatAddr()` becomes the invite shared out-of-band.

`Dial` parses the invite with Tailcat, creates a `tailcat.Client`, and calls `DialTCPPort` for port `8411`. The context bounds initial connection setup to 30 seconds. Once connected, Catpult uses the returned `net.Conn`; client lifetime remains attached to the connection and is closed by the receiver.

## Relay selection

- Default: Tailcat's default DERP map and region selection.
- `--derp H[,H...]`: Catpult builds a Tailcat `DERPRegion` with one node per endpoint. Endpoints are bare host/host:port values; Tailcat uses DERP over TLS, default port 443. This bypasses DERP map fetching for that server.
- `--derpmap URL`: sets Tailcat's `DERPMapURL` to an alternate JSON map.
- The options are mutually exclusive. Tailcat address generation carries the selected relay configuration so the recipient does not pass relay flags.

Direct and DERP-relayed paths use the same Tailcat WireGuard transport. DERP is a relay path, not a Catpult application server; custom DERP changes relay selection, not the Catpult wire protocol.

## Logging and shutdown

Tailcat logs are discarded unless `CATPULT_DEBUG` is set. Sender shutdown stops accepting work at the application layer, closes active connections, then calls `DrainTCP` with a one-second bound before `Server.Close`. Receiver close similarly drains the client after finite completion when possible. Tailcat's drain exists because its TCP stack runs in-process: an immediate process exit can otherwise lose the final FIN/ACK. Drain is best effort and bounded so an unreachable peer cannot hold shutdown indefinitely.

Tailcat reference points are `github.com/tailscale/tailcat`'s `Server.Start`, `Server.OnTCP`, `Server.DrainTCP`, `Server.Close`, `Client.DialTCPPort`, and `Client.DrainTCP`. Catpult's adapter is in [`internal/transport/transport.go`](../../internal/transport/transport.go).
