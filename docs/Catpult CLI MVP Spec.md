# Catpult CLI MVP

## CLI

### Sender: files

```bash
catpult [--expires DURATION] FILE...
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
