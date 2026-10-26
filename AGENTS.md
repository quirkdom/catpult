# AGENTS.md

## Build / verify

Requires Go 1.27+ (`go version` → `go1.27.1 darwin/arm64`).

```sh
make build       # host binary -> ./catpult
make build-all   # cross-compile PLATFORMS -> dist/ (linux, windows, darwin; amd64 + arm64)
make package     # cross-compile and package into archives (.tar.gz/.zip) in dist/
make checksums   # generate SHA256 checksums in dist/checksums.txt
make check       # gofmt gate + vet + test + build-all
make clean       # remove dist/ and ./catpult
make deep-clean  # clean build outputs + purge Go build & test caches
make help
```

`Makefile` is the source of truth for build flags: `-trimpath`, `-ldflags '-s -w -X main.version=$(VERSION)'`, `CGO_ENABLED=0`. `VERSION` defaults to `git describe` (falls back to `dev`); override with `make VERSION=v1.2.3 build-all`. Per-platform: `make build-one PLATFORM=windows/arm64`. Raw commands still work:

```sh
go build -o catpult ./cmd/catpult
./catpult help
./catpult version
go vet ./...
go test ./...   # tests exist in internal/protocol, internal/room, cmd/catpult
```

Module `catpult`. Tailcat is now a direct dependency (`github.com/tailscale/tailcat v0.6.0`).

## Releasing

See [`RELEASING.md`](RELEASING.md) for the end-to-end release process: cutting a GitHub tag, GitHub Actions matrix packaging, and updating the Homebrew tap formula for bottling.

## Layout (current)

- `cmd/catpult/main.go` — CLI entrypoint with `run()`, args parsing (`--expires`, `--target`), sender (files/stdin stream), receiver, usage.
- `cmd/catpult/main_test.go` — CLI tests exist, plus live local-DERP e2e in `derp_e2e_test.go` (`TestCustomDERPMapE2E`, `TestCustomDERPEndpointsE2E`: spins up an in-process DERP+STUN server, runs real sender/receiver processes through `--derpmap` / `--derp`; skipped under `-short`).
- `internal/protocol/` — magic-prefixed (`EGAD`), versioned, length-delimited framing (`protocol.go`, `protocol_test.go`). Defines `magic`, `Version`, `MsgType`, frame read/write, `ROOM_INFO`, `FILE_HEADER` encoding.
- `internal/transport/` — transport abstraction (`transport.go`). Uses Tailcat Go library directly; defines `NewServer` (+ `Relay` for the two custom-relay mechanisms), `Dial`, `IsKey`, timeouts. Set `CATPULT_DEBUG=1` to surface relay logs (`debugLogf`).
- `internal/room/` — room lifecycle (`sender_file.go`, `sender_stream.go`, `receiver.go`, `safety.go`, `room_test.go`). Handles file serving, streaming broadcaster, receiving with path validation.
- `docs/` — `Catpult CLI MVP Spec.md` (authoritative CLI contract) and `Catpult - Agent Implementation Specification.md` (product/protocol spec). Read both before touching CLI/transport/protocol/room.
- `go.mod` / `go.sum` — `catpult`; `tailcat` direct; `go 1.27.1`.

`internal/` is no longer empty: protocol, transport, and room packages implemented.

## Tailcat reference (local)

- Tailcat repo cloned at `~/Developer/tailcat` — read Go API source there.
- `tailcat` CLI installed (`/opt/homebrew/bin/tailcat`). Use `tailcat readme`.

## Architecture invariants (unchanged — do not violate)

- No Catpult backend: no DB, API server, auth service. Sender process is the room host; room dies on sender exit or TTL expiry. No persistence beyond the process.
- Transport: use the Tailcat **Go library** directly (`transport` package). Never `exec("tailcat ...")`. Never implement NAT traversal, WireGuard, relay, or custom encryption over Tailcat.
- Protocol stays minimal, magic-prefixed, versioned, length-delimited, streamable. Every frame: `EGAD` magic + version byte + type byte + uvarint length + payload. The magic is product identification, not authentication — the invite stays the access control. Validate framing (`protocol.ReadFrame`), enforce max sizes/timeouts (`MaxFrame`, `MaxHeader`, `ChunkSize`), reject malformed input gracefully.
- Artifacts are opaque bytes — never parse `.env`/JSON/PEM. Avoid staging to disk; if temp files unavoidable, use restrictive perms and clean up (`safety.go`).
- Recipient safety: validate filenames, reject absolute paths and `../`, never write outside intended receive dir (`target`), never expose FS contents or command execution (`receiver.go`, `safety.go`).
- Delivery is best-effort, no ACKs/receipts/queues. Report `sent` / `transfer completed`, never `delivered`.
- Invite is a secret bearer capability, not an "encryption key".
- MVP scope only: `catpult [--expires D] [--derp H[,H...]] [--derpmap URL] FILE...` / `catpult [--expires D] -` sender, `catpult KEY [--target DIR]` receiver. Out of scope: accounts, auth, vaults, history, receipts, browser/WASM, `--direct-only`, `--name`, recipient→sender transfer, REPL, multi-room, daemon/IPC, mixed file+stream. See spec §21.
