# Catpult — Implementation Plan (MVP)

Sources of truth:

- Product/protocol: `docs/Catpult — Agent Implementation Specification.md`.
- CLI contract (wins on conflict): `docs/Catpult CLI MVP Spec.md`.

Each step is hands-on; verify before moving on.

## 0. Invariants (do not violate)

- No Catpult backend: no DB, API server, auth. Sender process is the room host; room dies on sender exit or TTL expiry. No persistence beyond the process (bounded in-memory retention for the live room only).
- Single-process sender: `catpult [--expires D] FILE...` / `catpult [--expires D] -` / `producer | catpult`. No `create`/`join`/`send` subcommands, no daemon, no local IPC, no REPL, no multi-room.
- Receiver: `catpult KEY [--target DIR]`. Never consumes stdin. File mode writes files + prints filenames to stdout; stream mode writes raw bytes to stdout. Status/warnings/errors go to stderr; never block pipelines on confirmation.
- Transport: Tailcat **Go library** directly (`github.com/tailscale/tailcat v0.6.0`, pinned in `go.mod`). Never `exec("tailcat ...")`. Never implement NAT traversal, WireGuard, relay, or custom encryption over Tailcat.
- Protocol: minimal, magic-prefixed, versioned, length-delimited, streamable. Every frame starts with the `EGAD` magic (product identification, not authentication) then a version byte. `ROOM_INFO` announces `protocol_version` + `payload_type` (file|stream) so the receiver can warn on mode mismatch. Validate framing, enforce max sizes/timeouts, reject malformed input gracefully.
- Artifacts are opaque bytes — never parse `.env`/JSON/PEM. File and stream payloads never mix in one room. Avoid staging to disk; temp files only with restrictive perms + cleanup.
- Recipient safety: validate filenames, reject absolute paths and `../`, never write outside `--target DIR`, never expose FS or exec.
- Delivery is best-effort, no ACKs/receipts/queues. Every joiner while the room is alive gets the full payload (all files, or buffered + live stream bytes). Report `sent` / `transfer completed`, never `delivered`.
- Invite (`tc...` Tailcat addr) is a secret bearer capability, not an "encryption key".
- MVP scope only (spec §21 + CLI spec): sender file/stdin modes, `KEY [--target]`
  receiver, `--expires`, stdout/stderr split, mode-mismatch warnings. Out of scope: accounts, auth, vaults, history, receipts, browser/WASM, custom DERP, `--direct-only`, `--name`, recipient→sender transfer, REPL, multi-room, daemon/IPC, mixed payloads.
- Do not run `go mod tidy` until code imports tailcat (currently `// indirect`); it will drop the pin.

Local Tailcat reference: repo at `~/Developer/tailcat`, CLI at `/opt/homebrew/bin/tailcat`, full README via `tailcat readme`.

### Tailcat API cheat sheet (local clone `~/Developer/tailcat/tailcat.go`)

- `type Addr string` (:149) — invite/KEY, `tc...` base64url CBOR. Contains PSK; secret.
- `type Server struct` (:408), zero value usable. `OnTCP func(port uint16) func(net.Conn)` (:477, must set before Start; nil → RST). `Logf` defaults to `log.Printf`; use `logger.Discard` for quiet.
- `(s *Server) Start() error` (:567), `(s *Server) TailcatAddr() Addr` (:991, after Start only), `(s *Server) Close() error` (:862), `(s *Server) DrainTCP(ctx)` (:892 — call with timeout before exit or FIN can be lost).
- `type Client struct{ Server Addr }` (:1822), `NewClient(addr)` (:1878), `(c *Client) DialTCPPort(ctx, port)` (:2204, lazy tunnel on first dial), `Close` + `DrainTCP` (:922).
- Use `Addr` only; `ConnBlob` is deprecated.

### Target layout

```
cmd/catpult/main.go        # arg-shape dispatch: sender (files/-/pipe) vs receiver (KEY); --expires, --target, --help, --version
internal/transport/        # thin Tailcat wrapper: NewServer, Dial; single port const
internal/protocol/         # versioned framing: ROOM_INFO (version+type), ARTIFACT, STREAM_CHUNK/END, limits
internal/room/             # sender registry + broadcast + stream buffer; receiver writer
internal/cli/ (optional)   # TTY/pipe detection, stdout-vs-stderr routing, mismatch warnings
```

One TCP port for all Catpult traffic (pick once, e.g. `const Port uint16 = 80`, keep in `internal/transport`).

Sender/receiver disambiguation: single `KEY` (parseable Tailcat address) with no
file operands → receiver; file operands / `-` / piped stdin → sender. Ambiguous
input (`producer | catpult FILE`, `-` mixed with files, `something | catpult KEY`) is an error.

---

## Step 1 — Tailcat API recon (done / confirm)

- [ ] Read `Server`, `Client`, `Addr` definitions above.
- [ ] Read `cmd/tailcat` server/client modes as usage examples.
- [ ] Confirm: ephemeral key default, `TailcatAddr` after `Start`, `DialTCPPort` lazy, `Close`+`DrainTCP` lifecycle.

Verify: can describe invite generation → accept → dial → shutdown without looking.

## Step 2 — Raw spike: sender + KEY receiver with test line

Goal: prove transport + arg-shape dispatch, no protocol yet.

- [ ] `internal/transport`: `NewServer() (*tailcat.Server, Addr, error)` with `OnTCP` returning handler that writes `hello from catpult\n` then closes; `Dial(ctx, key)` via `NewClient` + `DialTCPPort`.
- [ ] `cmd/catpult`: dispatch sender (`FILE...`/`-`/piped stdin) vs receiver (`KEY [--target DIR]`); `--expires`, `--target`, `--help`, `--version`. Sender: Start → print invite to stderr (+ status) → block on SIGINT/expiry → `Close`+`DrainTCP`. Receiver: 30s dial timeout → `io.Copy(os.Stdout, conn)` → `Close`+`DrainTCP`. All status to stderr; stdout stays payload-clean.
- [ ] Manual E2E: `go build -o catpult ./cmd/catpult`; term A `echo hi > t.txt; ./catpult --expires 10m t.txt`, term B `./catpult <KEY>`; expect hello line.

Verify: `go vet ./...`, two-process E2E works, no Catpult server involved, `something | catpult KEY` rejected.

## Step 3 — Minimal versioned application protocol

Goal: replace raw line with framed messages.

- [ ] `internal/protocol`: `magic = "EGAD"`, `Version = 1`; `ROOM_INFO{protocol_version, payload_type}` always first; file mode `ARTIFACT{artifact_id, filename, size, payload}` (header + streamed payload, never buffer whole file); stream mode `STREAM_CHUNK{offset, payload}` + `STREAM_END`. Length-prefixed encoding (4-byte magic + 1-byte version + 1-byte type + uvarint length + payload); `MaxFrame`, `MaxHeader` constants.
- [ ] Strict decode: check version, `payload_type` ∈ {file, stream}, unknown type → error, length ≤ max, integer-overflow safe, short reads → error, timeouts via `SetDeadline`. Receiver learns type from `ROOM_INFO` and arms mismatch warnings.
- [ ] Unit tests: round-trip, truncated frame, oversized length, bad magic, bad version, unknown type, trailing garbage — all rejected gracefully.

Verify: `go test ./internal/protocol/`, no Tailcat needed.

## Step 4 — File mode: serve all files to every joiner

Goal: `catpult [--expires D] FILE...` → `catpult KEY [--target DIR]`.

- [ ] Sender retains payloads for room lifetime (re-read from disk per joiner or hold in memory; document choice). Each joiner gets `ROOM_INFO(file)` + every `ARTIFACT` in order.
- [ ] Recipient: decode → validate filenames (Step 7 rules) → stream each payload to `--target DIR` (default cwd) with 0600 → print each filename to stdout on success. Filenames only, never contents, even when piped.
- [ ] Sender stderr: per-file `Sending foo (N bytes)...` + `sent to N connected recipient(s)` — never `delivered`. Opaque bytes; never parse.
- [ ] Multi-file: `foo.txt bar.txt` both delivered in order; verify byte-identical (`cmp`/`shasum`) for 1-byte, empty, ~10 MB inputs.

Verify: 1:1 file E2E over real Tailcat; late joiner still gets all files.

## Step 5 — Stream mode + 1:N broadcast

Goal: `producer | catpult` / `catpult -` → `catpult KEY > out`; concurrent recipients.

- [ ] Sender: read stdin chunks → append to in-memory ring/cache (bounded; document cap) → broadcast `STREAM_CHUNK` to live conns. New joiners get `ROOM_INFO(stream)` + buffered bytes in order + live tail. Every recipient sees the same stream. stdin EOF does NOT close the room (per CLI spec strike-through); room ends on ^C/expiry.
- [ ] `room.Registry`: mutex-guarded `map[peerID]net.Conn`, add on accept, remove on close/error. Broadcast via snapshot + goroutine per conn + per-recipient deadline; slow/failed recipient never blocks others. No ACKs, retry, queue.
- [ ] Receiver stream path: raw bytes to stdout (`catpult KEY | command`, `> output`). Stderr mismatch warning when stream lands on a terminal; file-mode-to-pipe warning when filenames land on a pipe.
- [ ] No mixing: files + stream in one room is an error.

Verify: 1 sender + 2 concurrent receivers get identical streams; killing one mid-transfer doesn't break the other; `catpult KEY | shasum` matches sender input prefix.

## Step 6 — `--expires`, `--target`, graceful shutdown

Goal: lifecycle per CLI spec.

- [ ] `--expires` (Go duration parse, default e.g. 30m; `0` = until ^C). Sender closes on first of ^C/expiry: stop accepting, close conns, `DrainTCP` with timeout, destroy server, release state. Receiver exits on ^C / room closure-expiry / finite-transfer completion.
- [ ] `--target DIR`: created if missing (0700); all writes confined under it. Default cwd.
- [ ] Receiver never reads stdin; sender validates `FILE` vs `-` vs pipe exclusivity up front with usage errors.

Verify: `--expires 5s` shuts sender down alone; receivers see clean EOF; Ctrl-C both sides clean; no send after close.

## Step 7 — Security + resource limits + malformed input + I/O contract

Goal: spec §11 + CLI stdout/stderr/mismatch rules.

- [ ] Filename: reject absolute, `../`, backslash-absolute, empty, >255 bytes, NUL; join with target dir + `filepath.Clean` + prefix check; never write outside. Partial on size-mismatch → delete + cleanup.
- [ ] Sizes: `MaxArtifactBytes` (e.g. 100 MB), `MaxFilenameLen`, per-frame cap, stream-buffer cap; huge length prefix → abort conn, keep room alive.
- [ ] Timeouts: accept/dial/read/write deadlines (e.g. 30s handshake, 60s/file); malformed peer → close conn, keep room alive.
- [ ] I/O contract tests: stdout carries only filenames (file) or raw bytes (stream); stderr carries all status; piped receiver still emits filename manifest; `something | catpult KEY` and `producer | catpult FILE` error without side effects; mismatch warnings fire but never block pipes.
- [ ] Fuzz-ish tests: garbage KEY, wrong version, unknown payload_type, huge length, traversal names — rejected, no panic, no FS escape.

Verify: `go test ./...` includes adversarial + I/O-contract cases; manual bad-KEY errors cleanly.

## Step 8 — Integration tests over real Tailcat

- [ ] `internal/room` or `tests/`: ephemeral `Server` + N `Client`s (DERP bootstrap still networked; longer timeouts / `-short` skip).
- [ ] Cases: 1:1 file set, 1:2 broadcast identical, late joiner full payload, stream buffered+live prefix match, `--expires` closes, malformed frame drops conn not room, `--target` confinement.
- [ ] Keep hermetic unit tests separate from networked integration (`testing.Short()` gate).

Verify: `go test ./...` passes locally; `go test -short ./...` passes offline-ish.

## Step 9 — Docs + security statement

- [ ] `README.md`: install, sender file/stream examples, `KEY [--target]` receiver, `--expires`, stdout/stderr split, mismatch warnings, best-effort wording (`sent`, not `delivered`).
- [ ] Security notes: Tailcat provides WireGuard transport; Catpult adds framing/filename/path validation only. Do NOT claim unhackable / zero-knowledge / anonymous / securely-deleted / guaranteed-direct / no-metadata (spec §24).
- [ ] Note experimental Tailcat API (no stability promise) + pinned `v0.6.0`; upgrades are explicit.

Verify: fresh reader can run the MVP success path (spec §23) from README alone.

---

## Build order summary

1 → 2 (stop: E2E hello + dispatch) → 3 (stop: unit-tested framing with payload_type) → 4 (stop: 1:1 + late-join files) → 5 (stop: 1:N stream) → 6 (stop: TTL/target/shutdown) → 7 (stop: adversarial + I/O tests) → 8 (stop: integration) → 9 (stop: docs). Then Phase 2 only.
