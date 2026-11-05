# Catpult CLI Contract

This page describes current user-visible CLI behavior. Update it with any change to command syntax, output, or lifecycle. Architecture and implementation details live in [`architecture/`](architecture/README.md); the implementation is in [`cmd/catpult/main.go`](../cmd/catpult/main.go).

## Commands

```text
catpult [-q|--quiet] [--expires DURATION | --no-expiry] [--derp HOSTS | --derpmap URL] FILE...
catpult --watch [-q|--quiet] [--expires DURATION | --no-expiry] [--derp HOSTS | --derpmap URL] FILE...
catpult [-q|--quiet] [--expires DURATION | --no-expiry] [--derp HOSTS | --derpmap URL] -
producer | catpult [-q|--quiet] [--expires DURATION | --no-expiry]
catpult [-q|--quiet] KEY [--target DIR]
```

Value-taking flags accept either `--flag VALUE` or `--flag=VALUE`. `--quiet`, `--no-expiry`, and `--watch` are bare boolean flags. Options may appear before or after operands; `--` ends option parsing. `help`, `-h`, `--help`, `version`, `-v`, and `--version` are supported.

The default room lifetime is 30 minutes. `--expires` accepts a non-negative Go duration such as `10m` or `1h`; `0` disables expiry. `--no-expiry` is equivalent to zero. `--expires` and `--no-expiry` cannot be combined.

## Sending files

Named files must be regular files. Each recipient connection receives the named files in order, served from disk. The receiver writes each file under its basename. The sender continues to accept recipients until expiry or sender shutdown (`Ctrl+C`/termination signal).

File arguments and stdin are mutually exclusive. `producer | catpult FILE` and `catpult - FILE` are errors.

## Watching files

`--watch` keeps each recipient connected and resends the named files when their mtime or size changes. Detection polls about once per second; `touch` can trigger a resend. The initial watched path set is fixed; new paths are not discovered. Missing files are skipped until they reappear. A change during a send aborts that version at a chunk boundary and restarts from the beginning; rapid changes may coalesce.

The receiver needs no watch flag: the room announces watch mode. It prints a filename to stdout after the first successfully received version of that file; later versions are reported on stderr. It remains connected until the room closes or the receiver disconnects. Watch mode accepts only named files, not stdin or a receiver key.

## Sending stdin

Use `-` or pipe stdin. Input is opaque bytes. Catpult retains a rolling 64 MiB in-memory history for late joiners and sends live chunks to connected recipients. Older bytes are evicted when the limit is exceeded.

At stdin EOF, connected recipients receive the remaining queued bytes and a stream-end marker. Late joiners can receive the retained history and then the end marker. EOF does not close the room; it remains open until expiry or sender shutdown.

## Relays

`--derp HOSTS` configures one or more self-hosted DERP endpoints, comma-separated as `host[:port]` or `[IPv6]:port`; the default port is 443. Values are hostnames or IPs, not URLs. Tailcat uses DERP over TLS, and endpoints need valid certificates for their hostnames. This mode supplies a relay region directly instead of fetching a DERP map.

`--derpmap URL` uses an alternate JSON DERP map URL. Both `http` and `https` schemes are accepted. `--derp` and `--derpmap` are mutually exclusive sender options, including in watch mode. The invite carries the selected relay configuration, so receivers do not need relay flags. Direct and DERP-relayed connections use Tailcat's encrypted transport; neither mode guarantees a direct network path.

## Receiving

The receiver connects using the invite key. It does not consume stdin; `something | catpult KEY` is an error. `--target DIR` selects the receive directory and defaults to the current directory. Missing target directories are created. Received files use mode `0600`; path validation and rooted filesystem operations confine writes to the target.

File mode writes filenames to stdout, one per successfully completed file. Watch mode prints each filename on its first received version; update notices go to stderr. Stream mode writes raw bytes to stdout. For example:

```sh
catpult KEY --target ./received
catpult KEY > output.bin
catpult KEY | command
```

The receiver exits after a finite file transfer, stream end/connection closure, room closure, or local shutdown signal. Watch receivers stay connected until room closure or disconnect.

## Output and errors

Receiver stdout contains filenames in file mode or raw bytes in stream mode. Sender stdout contains the invite: a one-line invite when stdout is not a terminal or `--quiet` is set; otherwise a formatted invite summary. Human status, warnings, and errors go to stderr. `--quiet` suppresses non-fatal status and progress messages on stderr; it does not suppress the invite or errors.

The receiver warns on stderr when its stdout context may be surprising: file mode with redirected/piped stdout (which receives filenames), or stream mode with terminal stdout (which receives raw bytes). Warnings do not block pipelines or ask for confirmation.

Delivery is best effort. Catpult reports `sent`, `received`, or `transfer completed`; it does not provide delivery receipts or prove that a recipient retained a file. The invite is a secret bearer capability: share it out of band.

## Out of scope

Accounts, authentication services, persistent delivery/history, multi-room management, a daemon or local IPC, mixing files and streams in one room, browser/WASM support, direct-only mode, recipient-to-sender transfer, and remote execution are outside the current CLI contract.
