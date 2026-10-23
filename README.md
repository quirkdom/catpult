# Catpult

Ephemeral, peer-to-peer sharing of sensitive artifacts (`.env` files,
credentials, certificates) with no Catpult server. The sender's process is
the room host; recipients join with a bearer invite shared out of band.
Tailcat provides the encrypted transport; Catpult provides the room and a
minimal artifact protocol.

Requires Go 1.27+.

```sh
go build -o catpult ./cmd/catpult
```

## Sender

Share files (every recipient that joins while the room is alive gets all files):

```sh
catpult --expires 10m .env
catpult --expires 10m .env cert.pem config.json
```

Share a byte stream (recipients get buffered bytes + the live tail; stdin
EOF does not close the room):

```sh
catpult --expires 10m -
producer | catpult --expires 10m
```

The sender prints the invite to **stderr**, waits for recipients, and keeps
accepting them until `--expires` (default `30m`, `0` = until `^C`) or `^C`.
Exiting destroys the room; nothing is retained.

## Receiver

```sh
catpult KEY --target ./inbox
catpult KEY > output          # stream mode: raw bytes to stdout
catpult KEY | command         # stream mode composes with pipes
```

- File mode materializes files under `--target DIR` (default: cwd) with
  `0600` permissions and prints the received **filenames** to stdout, one
  per line (same manifest when stdout is piped).
- Stream mode writes raw received bytes to stdout.
- The receiver never consumes stdin: `something | catpult KEY` is an error.

## stdout / stderr

- **stdout:** payload output only — filenames (file mode) or raw bytes
  (stream mode). Safe to pipe or redirect.
- **stderr:** all status, progress, warnings, errors.

Mode/context mismatches warn on stderr without blocking pipelines:

- file sender + piped/redirected receiver → stdout holds filenames, not contents
- stream sender + terminal receiver → raw bytes go to the terminal

## Delivery semantics

Best effort. No ACKs, receipts, queues, or retransmission beyond what
Tailcat/TCP provides. Status says `sent` / `transfer completed` — never
`delivered`. Late joiners receive the full payload (all files, or buffered
+ live stream bytes up to a 64 MiB in-memory cap, oldest evicted first).

Artifacts are opaque bytes: Catpult never parses `.env`/`JSON`/`PEM`.
File and stream payloads never mix in one room.

## Security notes

- Payloads travel through Tailcat's WireGuard transport. Catpult adds no
  encryption layer of its own.
- Application-level safety: magic-prefixed, versioned length-delimited
  framing (the magic identifies Catpult traffic, it does not authenticate
  the peer), max sizes (1 MiB frames, 100 MiB files), timeouts, strict
  filename validation (absolute paths, `..`, NUL, and escapes from
  `--target DIR` rejected), partial files removed, no filesystem
  exposure, no command execution.
- The invite (`tc...` Tailcat address) is a **secret bearer capability**,
  not an "encryption key". Anyone holding it can receive while the room
  is alive.
- What Catpult does **not** claim: unhackable, zero-knowledge, anonymous,
  securely-deleted, guaranteed-direct-P2P, no-metadata. Tailcat's API is
  experimental with no long-term compatibility promise; Catpult pins
  `github.com/tailscale/tailcat v0.6.0` and treats upgrades as explicit
  dependency bumps.

## Layout

```
cmd/catpult/main.go      CLI: sender/receiver dispatch, lifecycle
internal/transport/      thin Tailcat wrapper (single TCP port 8411)
internal/protocol/       magic-prefixed framing: ROOM_INFO, file/stream chunks
internal/room/           file serving, stream broadcast buffer, receiver
```
