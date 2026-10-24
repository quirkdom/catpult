# Catpult

Ephemeral, peer-to-peer sharing of sensitive artifacts (`.env` files,
credentials, certificates) with no Catpult server. The sender's process is
the room host; recipients join with a bearer invite shared out of band.
Tailcat provides the encrypted transport; Catpult provides the room and a
minimal artifact protocol.

## Installation

Building from source is recommended. It only requires Go 1.27+ (Go automatically downloads all dependencies during the build - no extra package setup needed).

### Build from source

```sh
# Clone the repository
git clone https://github.com/quirkdom/catpult.git
cd catpult

# Build the binary
go build -o catpult ./cmd/catpult

# Or using make:
make build
```

If you prefer installing directly into your `$GOBIN` or `$GOPATH/bin`:

```sh
go install ./cmd/catpult
# or: make install
```

### Pre-built binaries

You can also grab a pre-built binary for your platform from the Releases tab. On macOS, because downloaded binaries are not notarized yet, Gatekeeper will flag the binary. You can clear the quarantine attribute and make it executable:

```sh
xattr -d com.apple.quarantine ./catpult
chmod +x ./catpult
```

## How To Use: Sending

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

## How To Use: Receiving

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

- **stdout:** payload output only  -  filenames (file mode) or raw bytes
  (stream mode). Safe to pipe or redirect.
- **stderr:** all status, progress, warnings, errors.

Mode/context mismatches warn on stderr without blocking pipelines:

- file sender + piped/redirected receiver → stdout holds filenames, not contents
- stream sender + terminal receiver → raw bytes go to the terminal

## Delivery semantics

Catpult is designed around a simple, lightweight, best-effort delivery model:

- **Direct peer-to-peer delivery:** Data is sent directly peer-to-peer across Tailcat's WireGuard/TCP connections. Catpult reports `sent` or `transfer completed` when bytes have been pushed across the connection - never `delivered`, since it doesn't wait for recipient application-level confirmations.
- **Late joiners welcome:** Anyone who joins while the room is open receives the payload. For file rooms, recipients get the full set of files. For stream rooms, they get buffered history (up to a 64 MiB in-memory buffer, with oldest bytes smoothly rolling off if exceeded) followed by the live stream tail.
- **Pure opaque bytes:** Catpult treats all payloads as opaque bytes. It never inspects or parses your `.env` configs, JSON, PEM certificates, or scripts.
- **One room, one purpose:** File transfers and stream transfers are cleanly separated - a room is dedicated to either files or streaming data, never both.

## Security notes

- **End-to-end transport encryption:** All traffic travels through Tailcat's WireGuard-encrypted channels directly between peers (or relayed via DERP when direct P2P connectivity isn't feasible). Catpult doesn't layer redundant custom encryption on top of WireGuard.
- **Bearer invite safety:** The room invite (a `tc...` Tailcat address) is a **secret bearer capability**. Think of it like a temporary access pass: anyone holding the invite can join and receive payloads while the sender's room is alive. Always share invites over an out-of-band, trusted channel (like Signal or direct chat). Once the room expires or the sender exits, the room vanishes completely.
- **Application-level guardrails:**
  - **Filesystem protection:** Strict path sanitization ensures received files cannot escape your chosen `--target` directory (rejecting `../`, absolute paths, null bytes, and path traversals).
  - **Safe file writes:** Files are written with restricted permissions (`0600`), and incomplete files from interrupted transfers are automatically cleaned up.
  - **Bounded resource limits:** Protocol framing enforces a 1 MiB frame cap, a 100 MiB file size limit, and active idle timeouts to keep things responsive and prevent resource exhaustion.
  - **No remote execution:** Catpult never exposes your local directory contents or executes commands.
- **Clear expectations:** Catpult is built for quick, practical peer-to-peer sharing between peers who intentionally share the invite token. It doesn't claim to be zero-knowledge, anonymous, or immune if an endpoint itself is compromised. Catpult pins tested releases of `tailcat` (`v0.7.0`) to keep the transport rock-solid.

## Credits
Catpult is built on top of [Tailcat](https://github.com/tailscale/tailcat), from the legendary Brad Fitz and co. at Tailscale.

## License

I haven't decided on an open license yet; source copyright applies for now. In the meantime, you have express rights and permission to build, run, and use Catpult however you wish.

Please note that this software is provided as-is, without any warranty, guarantee, liability, or support expectations from the author or Quirkdom.
