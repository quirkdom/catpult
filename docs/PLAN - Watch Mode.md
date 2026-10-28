# Catpult — Implementation Plan (Watch Mode)

> Status: **implemented and shipped** (supplemented by review fixes: `curFile`
> EOF guard + watch chunk deadlines in `receiveFiles`, mid-send truncation →
> `errFileChanged` in `writeFile`, reappearance-as-change and lock-free atomic
> versions in `FileWatcher`, unified `runFileSender` host with close-conns-
> before-wait shutdown). This document describes the feature as built.

Sources of truth:

- Product/protocol: `docs/Catpult — Agent Implementation Specification.md`.
- CLI contract (wins on conflict): `docs/Catpult CLI MVP Spec.md`.
- Build/verify: `AGENTS.md`.

This plan adds a **watch mode** to the file sender: the room keeps every
recipient connection open and re-sends a watched file whenever it changes on
disk. It builds on the shipped MVP file/stream modes; it does not change them.

## Goal / acceptance criteria

Sender side:

1. Sender can start a room in watch mode over the files passed as `FILE...`.
2. `--watch` flag selects watch mode.
3. Default expiry stays **30m** in watch mode; `--expires` overrides (`0` = until `^C`).
4. When a watched file changes, the new version is sent to all connected recipients.
5. If a file is in flight to a recipient when it changes, the in-flight transfer
   is dropped and a new one begins.
6. The sender does **not** close a recipient connection while the room is alive
   (another version may still be sent).
7. The sender closes the room on `^C` (or expiry) as in the MVP.

Receiver side:

1. Receiver keeps receiving / stays open until the sender room closes.
2. Receiver can stop with `^C` at any time.

## Invariants (do not violate)

- No backend, no persistence, no daemon/IPC: the sender process is still the room
  host; the room dies on sender exit or TTL expiry.
- Transport stays the Tailcat **Go library** only; no `exec`, no custom relay or crypto.
- Protocol stays minimal, magic-prefixed (`EGAD`), versioned, length-delimited.
  The magic is product identification, not authentication; the invite is the access control.
- Artifacts stay opaque bytes; no parsing of `.env`/JSON/PEM.
- Recipient safety unchanged: reject absolute paths / `../` / NUL, confine under
  `--target DIR`, `0600` file perms, remove partial files.
- Delivery is best-effort: report `sent` / `transfer completed` / `room closed`,
  never `delivered`.
- Watch mode is file-only. Files and streams never mix in one room.

## Design decisions (confirmed)

- **Change detection:** poll each watched path's `mtime` + size every ~1s.
  No new dependencies; cross-platform; tolerates editors that save via atomic rename
  (we re-`stat` the path each tick, never an open handle). Detection latency ≤ poll interval.
- **In-flight supersede (req 5):** implicit. The sender aborts the current file at a
  chunk boundary and starts a fresh `FILE_HEADER`; a `FILE_HEADER` seen while the
  receiver is mid-file means "discard the partial and start over". No new wire
  message and no `Version` bump.
- **Receiver manifest:** print the filename to stdout each time a version is fully
  received, so `catpult KEY | ...` observers see every update. A stderr note marks
  subsequent versions as updates.
- **Room mode discovery:** a new ROOM_INFO payload type (`PayloadFileWatch`) tells the
  receiver it is in watch mode, so it can (a) clear the idle read deadline while waiting
  between versions (chunk reads carry the usual per-frame deadline), and (b) map EOF
  to `room closed` rather than `transfer completed`.

## Wire change (small, backward compatible)

In `internal/protocol/protocol.go`:

```go
const (
    PayloadFile      PayloadType = 0x00
    PayloadStream    PayloadType = 0x01
    PayloadFileWatch PayloadType = 0x02 // file mode + keep-open/resend semantics
)
```

- `PayloadType.String()` → `"file-watch"`.
- `DecodeRoomInfo` accepts it; the ROOM_INFO payload stays exactly 1 byte, so
  `Version` stays `1`. (The existing test asserting a 2-byte ROOM_INFO is invalid
  remains correct.)
- Only watch rooms emit it. Old receivers fail cleanly with
  `unknown payload_type 0x02`; finite and stream interop is untouched.

## `internal/room/watch.go` (new)

```go
const WatchPollInterval = 1 * time.Second

// FileWatcher polls watched paths and bumps a per-path version on any change.
// versions hold sync/atomic counters, so the per-chunk hot-loop check
// (w.Version) never contends with the poller's mutex.
type FileWatcher struct { /* mu, order, sigs, versions (atomic counters), missing, subs, stop, done */ }

func NewFileWatcher(paths []string, interval time.Duration) (*FileWatcher, error)
func (w *FileWatcher) Start()                                          // start the poll goroutine
func (w *FileWatcher) Version(path string) uint64                      // lock-free atomic load (0 = unknown)
func (w *FileWatcher) SetChangeCallback(fn func(name string))          // bump notification (fired outside mu)
func (w *FileWatcher) SetWarnCallback(fn func(format string, ...any))  // diagnostics sink (default: discard)
func (w *FileWatcher) Subscribe() *Subscription
func (w *FileWatcher) Close()                                          // stop polling and close all subs

// Subscription is one recipient connection's view of changes.
type Subscription struct { /* w, notify chan struct{}, done chan struct{} */ }

func (s *Subscription) Changes() <-chan struct{} // coalesced "something changed"
func (s *Subscription) Done() <-chan struct{}    // watcher closed
func (s *Subscription) Close()

// ServeWatchConn serves one recipient for the room's lifetime.
func ServeWatchConn(conn net.Conn, w *FileWatcher, files []string, onEvent func(WatchEvent)) error
```

- Poll loop: `stat` each path; if `modTime` or size differs from the last signature —
  **or the path reappears after a disappearance** (even with a unchanged signature) —
  bump that path's version. Callbacks and subscription signals fire *outside* the
  poller lock (deadlock hazard); signals are non-blocking and coalesced via a
  1-buffer channel — a slow/blocked recipient never stalls the poller or other
  recipients.
- A missing/deleted watched file is reported once via the warn callback and skipped,
  not fatal (it may reappear via an atomic save). Version is preserved until it does;
  disappearance-to-reappearance transitions bump the version so recipients get a
  fresh send.
- `ServeWatchConn` closes its conn when serving ends (room close via `Done()`,
  recipient gone via write error) — the same close-on-end semantics as file mode.
- `sendWatchedFile(conn, w, path, lastSent, closing, onEvent)`: capture
  `ver := w.Version(path)`, send with a per-chunk `changed()` check; if a newer
  version appears mid-send, return a sentinel (`errFileChanged`) **without**
  `FILE_END` and restart with the new version. A concurrent rewrite that races the
  poller — short read (`sent != size`) with no version bump yet — maps to
  `errFileChanged` too (a short read on an open regular file *proves* concurrent
  truncation; do not wait a poll interval to confirm). After a clean `FILE_END`,
  re-read the version and resend if it changed during the final moments. Only update
  `lastSent[path]` after a clean send. On `closing()`, abort at a chunk boundary and
  end cleanly (`errWatcherClosed` → no serving error).
- Shared chunk sender: factor the read/chunk/`FILE_END` loop out of `sendOneFile`
  into `writeFile` with an optional `changed func() bool` abort hook, used by both
  the finite and watch paths (keeps the size-consistency checks in one place).

## `internal/room/sender_file.go`

- Add the shared chunk helper (above); keep `ServeFileConn` and finite behavior
  byte-for-byte identical (`changed == nil`).
- Mid-send concurrent truncation on the watch path: a short read on an open regular
  file (loop exits with `sent != size` after `io.EOF`, or an accidental read error)
  maps to `errFileChanged` whenever a change hook is installed — supersede, not a
  fatal error, so a rewrite racing the one-second poller cannot sever the recipient
  connection.

## `internal/room/receiver.go`

- Add `case protocol.PayloadFileWatch:` in `Receive` → `receiveFiles(conn, targetDir, stdout, watch=true)`.
- One flattened state machine inside `receiveFiles` (shared by finite and watch):
  - `MsgFileHeader` between files → open the new file (validate name, `os.OpenRoot`,
    `0600`, `O_TRUNC`).
  - `MsgFileHeader` mid-file → discard the partial (`os.Remove`), then start the new
    header. This is the implicit-supersede path (req 5).
  - `MsgFileChunk` → validate against the declared size (overflow → error), write.
  - `MsgFileEnd` → verify byte count (truncation → error), close, print the filename
    to stdout, mark complete.
  - Any other/unexpected frame → error, as today.
  - All abort/error paths keep the existing partial-file cleanup.
- EOF is only a clean termination when no file is open (`curFile == nil`):
  - connection closed mid-transfer → `receiving %s: … io.ErrUnexpectedEOF` (never
    success — a partially-transferred file must not read as a completed transfer);
  - watch mode → `ErrRoomClosed`;
  - finite mode with at least one completed file → clean completion (`nil`).
  A torn frame (`io.ErrUnexpectedEOF` from the reader) is truncation, never a clean
  end, even between files.
- Deadlines:
  - finite mode: keep the 60s `frameTimeout` per read.
  - watch mode: cleared while idle between versions (gaps are normal, EOF →
    `ErrRoomClosed`), but **applied during chunk reads** (`curFile != nil`) so a
    sender that starts an update and hangs aborts at `frameTimeout` instead of
    stalling forever; a deadline-set failure on a dead conn is classified through
    the same EOF logic.
- `receiveStream` is untouched.

## `cmd/catpult/main.go`

- `options.watch bool`; parse `--watch` (boolean). `--watch=<x>` falls through to the
  existing unknown-flag error.
- Validation in `run` (before dispatch):
  - `--watch` with a KEY (receiver) → error: sender option.
  - `--watch` with `-` or piped stdin → error: files only.
  - `--watch` with no operands → error (usage).
  - `--watch --target` still hits the existing `--target is a receiver option` error.
- `runFileSender(expires, relay, files, stderr, watch)` (unified host for finite and
  watch file rooms; the sole watch difference is the `FileWatcher` + banner wiring and
  which handler closure runs): validate regular files; build + `Start()` the watcher
  (watch mode); build the handler that calls `room.ServeWatchConn` or
  `room.ServeFileConn`, tracks conns in a mutex map, and counts into a handler
  WaitGroup; print the invite (noting watch mode and its expiry); block on
  `waitForShutdown(expires)`; then shutdown order: `watcher.Close()` (flips handlers'
  `closing()` gates, no new resend pass starts) → **close tracked conns first** (so
  in-flight writes to a stalled recipient abort immediately instead of hanging
  teardown) → `wgHandlers.Wait()` (each handler closes its own conn as it exits) →
  `srv.DrainTCP(ctx)` → `srv.Close()` → `room closed (reason).`
- stderr lines (all best-effort, never `delivered`):
  - `recipient N connected.`
  - `Sending <name> (<n> bytes)...` per version;
  - `<name> changed; resending to <k> recipient(s).` when a change is detected;
  - `watched file <name> is missing; skipping until it reappears` (recipient-visible
    event) and the watcher's own one-shot warn line for a vanished path.
- `runReceiver` needs no new logic: `Receive` returns `ErrRoomClosed` in watch mode on
  room close (prints `room closed.`), and `^C` already terminates the process.
- Update `usage` with the `--watch` line and constraints.

## Tests

- `internal/protocol/protocol_test.go`
  - round-trip `EncodeRoomInfo(PayloadFileWatch)`;
  - `DecodeRoomInfo` accepts it; unknown still rejected; 2-byte still rejected.
- `internal/room/room_test.go` (new cases)
  - `FileWatcher` bumps version on `mtime`/size change and coalesces multiple signals.
  - watch round trip over `net.Pipe`: receive initial content, rewrite the file,
    receive the new version, and assert the stdout manifest shows the name twice;
    room close maps to `ErrRoomClosed`, not a completed transfer.
  - implicit supersede: feed `FILE_HEADER` + partial `FILE_CHUNK`s + a second
    `FILE_HEADER` + full content + `FILE_END`; assert the final file holds the new
    version and no partial remains.
  - missing-file handling: a vanished watched file is skipped (warn callback, no
    fatal error) and reappearance triggers a fresh send.
- `cmd/catpult/main_test.go`
  - `--watch` parses; `--watch` + receiver KEY / `-` / no files → errors.
- `cmd/catpult/watch_e2e_test.go` (networked, `-short` skip; separate file from the
  DERP e2e)
  - `TestWatchE2E`: two real sender/receiver processes over the default relay with
    `--watch`; the receiver observes the initial send and a re-sent version, and
    prints `room closed.` at teardown.
- `cmd/catpult/derp_e2e_test.go` (networked, `-short` skip)
  - local DERP+STUN harness e2e for finite mode and custom relay endpoints;
    unchanged by watch mode.

## Docs

- `docs/Catpult CLI MVP Spec.md`: add a "Sender: watch" section (signature, `--watch`,
  re-send semantics, lifecycle) and note the receiver stays until room close.
- `docs/FEATURE_MATRIX.md`: mark file watch mode available (File Sharing row).
- `README.md`: a `--watch` example and behavior note.
- `AGENTS.md`: note `internal/room/watch.go` and the `PayloadFileWatch` payload type.

## Verify

- `make check` (gofmt gate + vet + test + cross-build).
- Manual two-terminal smoke test over a real relay: `catpult --watch .env`, receiver
  joins, edit `.env`, confirm the receiver writes the new version and prints the
  filename again; `^C` on the sender closes the room and the receiver exits.

## Out of scope / notes

- **Stream idle deadline:** `receiveStream` also sets a 60s idle read deadline, so a
  producer quiet for >60s can time out the stream. Watch mode sidesteps this by
  clearing the read deadline while idle between versions; fixing the stream path is
  separate and not part of this plan.
- No new files, only the `FILE...` set passed at room creation is watched.
- Detection is `mtime`+size, so a `touch` with unchanged contents triggers a resend
  (acceptable; content hashing is not requested).
