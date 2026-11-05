# Sender modes

Argument shape selects exactly one sender mode: named files, watched named files (`--watch`), or stdin (`-` or piped stdin). File operands cannot be combined with stdin; watch mode only accepts named files. All modes create a Tailcat server, print its invite, accept recipients until expiry or shutdown, and serve each recipient independently.

## Finite file send

`runFileSender(..., false)` validates each source as a regular file, then `transport.NewServer` dispatches each accepted connection to `room.ServeFileConn`. Each recipient gets `ROOM_INFO(file)` followed by every file, re-read from disk for that recipient. `writeFile` sends a header, bounded chunks, then an end marker. A recipient failure affects only that connection. The handler closes after its batch, so later recipients can still join while the room is alive and receive the full set.

## Watch file send

`runFileSender(..., true)` creates a `FileWatcher` over the initial fixed path set. It polls mtime and size every second; added paths are not discovered, a touch can trigger a resend, and missing files are skipped until they reappear. Each accepted connection runs `room.ServeWatchConn`: initial file pass, an explicit initial-batch marker, then updates while the room remains open.

Each recipient has its own last-sent versions. A changed file aborts an in-flight version at a chunk boundary; the new header makes the receiver discard the partial and start the new version. Signals coalesce, so rapid changes can collapse into one resend. The connection remains open between versions; idle time is expected.

Shutdown stops the watcher first, closes active recipient conns, waits for handlers, then drains/closes Tailcat. This releases idle watch handlers as well as handlers in a send.

## Stdin stream send

`runSenderStream` starts a `room.Broadcaster` and reads stdin in protocol-sized chunks. `Publish` appends to an in-memory rolling history capped at 64 MiB and queues chunks to active recipients without blocking the input reader. Each recipient has one ordered writer. A slow recipient whose queue fills is dropped; it cannot stall other recipients or the producer.

At stdin EOF, input is marked ended and existing recipients receive queued bytes followed by `STREAM_END`; later recipients receive retained history and then end. EOF does **not** close the room. The room remains available until expiry or interrupt. Room shutdown sends end best-effort, closes the server, and performs bounded TCP drain.

## Operational boundaries

- Room default expiry is 30 minutes; `--no-expiry` means interrupt-only.
- `--derp` and `--derpmap` configure Tailcat bootstrap and are mutually exclusive. They do not change Catpult application framing.
- Status goes through the logger on stderr. Sender stdout always carries the invite: formatted for an interactive terminal unless `--quiet` is set, and one line otherwise. Sender payload bytes are never emitted.
- There are no delivery acknowledgments. “Sent” describes the local serving path, not proof that a recipient retained the artifact.

See [protocol](protocol.md) for message order and [transport](transport.md) for Tailcat details.
