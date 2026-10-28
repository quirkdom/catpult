# Catpult CLI MVP

## CLI

### Sender: files

```bash
catpult [--expires DURATION] [--derp URL] FILE...
```

- Starts an ephemeral sending server.
- Prints the room invite/key.
- Waits for recipients.
- Every recipient that joins receives **all files**.
- Continues accepting recipients until `--expires` or `^C`.
- Received files are materialized by the recipient.
- File arguments and stdin are mutually exclusive.

Example:

```bash
catpult --expires 10m foo.txt bar.txt
```

### Sender: files (watch mode)

```bash
catpult --watch [--expires DURATION] FILE...
```

- `--watch` selects watch mode; files only — combining it with `-` or
  piped stdin is an error, and using it with a KEY is an error (the
  receiver needs no flag: the room announces watch semantics itself).
- The file set is fixed: only the `FILE...` passed at room creation is
  watched; files that appear later are ignored.
- The default expiry stays **30m**; `--expires` overrides, `0` = until
  `^C`.
- Recipient connections are never closed while the room is alive: each
  watched file is re-sent to every connected recipient whenever its
  `mtime`+`size` changes on disk (polled about once a second; detection
  latency ≤ poll interval). A `touch` with unchanged contents also
  triggers a re-send.
- A version still in flight to a recipient when its file changes is
  dropped at a chunk boundary and replaced by a fresh version starting
  at the first byte; recipients converge on the latest contents.
- Consecutive rapid changes may coalesce into one re-send per poll.
- A watched file that disappears is skipped (logged once) until it
  reappears, e.g. via an editor's atomic save.
- The room's end is delivered to each connected recipient exactly like
  file mode: the sender closes the connection when its serving ends
  (watch serving ends only at room close or recipient disconnect), the
  receiver maps EOF to `room closed.`, then the sender prints
  `room closed (reason).`

Example:

```bash
catpult --watch .env
```

### Sender: stdin

```bash
catpult [--expires DURATION] -
```

or equivalently:

```bash
producer | catpult [--expires DURATION]
```

- `-` means read the payload from stdin, exactly like `cat`.
- Starts an ephemeral sending server and prints the invite/key.
- Incoming bytes are buffered in server cache.
- Recipients may join at any time and receive the buffered bytes followed by subsequent bytes.
- Every recipient receives the same byte stream.
- File arguments cannot be combined with `-`.
- `producer | catpult FILE` is invalid.

### Custom DERP relay

The sender's two modes extend with custom relay options:

```bash
catpult [--expires DURATION] [--derp H[,H...]] [--derpmap URL] FILE...
catpult [--expires DURATION] [--derp H[,H...]] [--derpmap URL] -
```

Two disjoint mechanisms, at most one per room:

- `--derp H[,H...]` — self-hosted relay endpoint(s): comma-separated
  `host[:port]` entries (or `[v6host]:port`), DERP over TLS on the given
  port (default 443). When set, no DERP map is fetched at all; this
  follows Tailcat's `genkey --region` comma-separated-hostname mechanism.
- `--derpmap URL` — fetch the room's bootstrap relay from the JSON DERP
  map at `URL` instead of the default map; otherwise as Tailcat's
  `--derpmap-url`. Both `http` and `https` are accepted (Tailcat imposes
  no https-only policy; the map is trusted infrastructure config).

Both are sender options using Tailcat's supported mechanisms; Catpult
implements no custom relay protocol. Combining them is an error.

- The chosen relay's details are embedded in the invite, so receivers take
  no relay flags. Using `--derp`/`--derpmap` with a KEY is an error.
- Self-hosted relay endpoints must present a valid TLS certificate for
  their hostname.
- Direct and DERP-relayed paths are equally encrypted; see the
  product spec (§16) for trust semantics.

### Receiver

```bash
catpult KEY [--target DIR]
```

- Joins the room identified by `KEY`.
- Blocks until the room expires, closes, or the client receives `^C`.
- Receiver does not consume stdin.
- `--target` specifies the directory for received files; default is the current working directory.

#### Receiving files

Files are written to the target directory.

Their filenames are emitted to stdout, one per line, after successful receipt.

Thus:

```bash
catpult KEY
```

prints:

```text
foo.txt
bar.txt
```

while creating:

```text
./foo.txt
./bar.txt
```

If stdout is piped/redirected, the same filename manifest is emitted.

The same contract holds in watch mode: the receiver stays connected and
prints the filename to stdout **each time a fully received version
lands** (once per version), with a stderr note marking subsequent
versions as updates; `room closed.` on stderr ends the session when the
sender's room closes, so `catpult KEY | ...` style observers see every
update.

#### Receiving a stream

The received bytes are written directly to stdout.

Therefore:

```bash
catpult KEY | command
catpult KEY > output
```

are valid and composable.

Receiver stdin is never consumed:

```bash
something | catpult KEY
```

is invalid.

## stdout / stderr

- **stdout:** payload output:
  - file mode → received filenames
  - stream mode → raw received bytes
- **stderr:** human-facing status, progress, warnings, and errors.
- Sender stdout/stderr may freely be piped or redirected.

## Mode mismatch warnings

The receiver knows the payload type from the room.

If the receiver's output context may be surprising, warn on stderr before delivery, e.g.:
- file sender + piped/redirected receiver → stdout contains filenames, not file contents.
- stream sender + terminal receiver → raw bytes will be written to the terminal.

Interactive confirmation may be used when appropriate; non-interactive pipelines must not be blocked waiting for confirmation.

## Lifecycle

Sender server closes on the first of:
- `^C`
- expiry
- ~~stdin EOF **for stream mode**, if the chosen MVP semantics treat EOF as completion.~~

Receiver exits on:
- `^C`
- room closure/expiry
- completion of a finite transfer.

## Explicitly out of scope

- REPL.
- Multi-room management.
- Persistent delivery / `--files`.
- Room administration/status commands.
- Daemon/local IPC.
- Mixing file and stream payloads in one room.
