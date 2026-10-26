# 🐱 Catpult

**The easiest, safest way to send secrets and files directly peer-to-peer.**

Ever needed to send a `.env` file, an SSH key, or a certificate to a teammate, but didn't want to paste it into Slack, email it, or upload it to Google Drive?

Catpult lets you send files and live terminal streams directly from your computer to someone else's. There are **no accounts**, **no central servers**, and **no cloud storage**. Everything travels over an encrypted WireGuard tunnel, and the moment you close Catpult, the transfer room vanishes without a trace.

---

## ⚡ 30-Second Quickstart

### 1. Send a file
Run `catpult` followed by the file you want to share:

```sh
catpult .env
```

Catpult creates a temporary room and prints a secret invite key:

```text
Room active! Invite key:
tc_a1b2c3...

Waiting for recipients (expires in 30m)...
```

Copy that invite key and share it with your teammate (over Signal, chat, or whatever you use).

### 2. Receive the file
On their machine, your teammate simply runs:

```sh
catpult tc_a1b2c3...
```

That's it! The `.env` file is downloaded directly to their current folder with private permissions (`0600`), and your sender terminal notifies you that the transfer completed.

---

## 📦 Installation

### Homebrew (macOS & Linux)

The recommended installation method for macOS and Linux. Installing through Homebrew automatically avoids macOS Gatekeeper notarization warnings:

```sh
brew install quirkdom/tap/catpult
```

### Pre-built binaries

Pre-compiled binaries for Linux, macOS, and Windows (Intel/AMD and ARM architectures) are available in the [Releases](https://github.com/quirkdom/catpult/releases) tab.

> **Tip for manual macOS downloads:** If downloading pre-built binaries directly outside of Homebrew, Gatekeeper will flag the unnotarized binary on first launch. You can clear the quarantine attribute with:
> ```sh
> xattr -d com.apple.quarantine ./catpult
> chmod +x ./catpult
> ```

### Build from source

Requires **Go 1.27+**:

```sh
# 1. Clone the repository
git clone https://github.com/quirkdom/catpult.git
cd catpult

# 2. Build the binary
make # or `go build -o catpult ./cmd/catpult`
```

To install directly to your `$GOPATH/bin`:
```sh
make install # or `go install ./cmd/catpult`
```

---

## 🚀 How to Use

### Sharing Files

You can share one file or a whole set of files at once:

```sh
# Share multiple files
catpult .env config.json cert.pem

# Set a custom timer (default is 30 minutes; use 0 to keep it open until Ctrl-C)
catpult --expires 10m .env

# Use your own relay infrastructure (e.g. enterprise / private network)
#
#   --derp: name self-hosted relay endpoint(s) directly (comma-separated,
#           HTTPS, default port 443)
catpult --derp relay-a.example.net,relay-b.example.net:8443 .env
#   --derpmap: or fetch a full DERP map from a URL
catpult --derpmap https://relay.example.net/derpmap.json .env
```

- **Late joiners welcome:** Anyone who enters the key while your terminal is running gets all the files.
- **Close anytime:** Hit `Ctrl+C` whenever you want to close the room immediately.

### Receiving Files

By default, files are saved into your current folder:

```sh
# Save to current folder
catpult KEY

# Save into a specific folder (created automatically if needed)
catpult KEY --target ./my-secrets
```

Catpult prints the saved filenames to your terminal, so you can easily see what arrived.

### Streaming Live Terminal Output & Pipes

Need to share a live log file, database backup, or command output in real time? You can pipe standard input straight into Catpult:

```sh
# Stream a file or command output
tail -f server.log | catpult
# or equivalently:
catpult - < server.log
```

On the receiving end, the bytes stream directly through standard output, making it easy to save to disk or pipe into another tool:

```sh
# Save the stream directly to a file
catpult KEY > server.log

# Pipe directly into a tool like jq or grep
catpult KEY | jq .
```

- **Stream catch-up:** If someone joins a few moments late, Catpult automatically replays up to 64 MiB of recent stream history so they don't miss anything.
- **Multiple listeners:** Several people can connect with the same key and watch the stream at the same time.

---

## 💡 Built for the Command Line

Catpult follows the classic Unix philosophy:
- **`stdout` (standard output)** carries only your data: saved filenames in file mode, or raw bytes in stream mode. This means commands like `catpult KEY | xargs ...` work smoothly.
- **`stderr` (standard error)** carries human messages: your invite keys, progress notices, connection status, and warnings.
- **Smart warnings:** If you accidentally stream binary data directly to your terminal screen, Catpult warns you on `stderr` without breaking your automated scripts.

---

## 🔒 How Catpult Keeps You Safe

- **End-to-End Encryption:** Traffic travels over WireGuard tunnels created with [Tailcat](https://github.com/tailscale/tailcat). If two computers cannot connect directly due to strict firewalls, traffic is securely relayed through encrypted relay nodes (DERP)—even the relay node cannot read your data.
- **Bring Your Own Relay:** Organizations that don't want to depend on public relay infrastructure can point a room at their own relay — either via `--derp` (self-hosted endpoint hostnames) or `--derpmap` (a full JSON DERP map), both sender options. The chosen relay's details are embedded in the invite, so recipients need no extra configuration — and the WireGuard encryption is identical either way.
- **Secret Invites:** The invite key (`tc...`) is a temporary pass. Only people who have the key can connect.
- **Strict Folder Guardrails:** Senders cannot sneak files into other folders on your computer. Catpult strictly blocks directory traversal tricks (`../`), absolute paths, and null bytes. Everything stays confined to your `--target` folder.
- **Clean Incomplete Transfers:** If a transfer is interrupted midway, partial files are automatically removed so you never end up with corrupted files.
- **No Background Footprint:** Catpult runs only while your command is active. There are no background daemons, local database files, or leftover services.

---

## 🗺️ Roadmap & Feature Matrix

Curious about what's coming in future releases (like web browser support, peer tracking, and direct-only modes)? Check out our comprehensive [Feature Matrix & Roadmap](docs/FEATURE_MATRIX.md).

---

## ❤️ Credits

Catpult is built on top of [Tailcat](https://github.com/tailscale/tailcat), created by Brad Fitzpatrick and the team at Tailscale.

---

## 📄 License

Source copyright applies for now. You have express rights and permission to build, run, and use Catpult however you wish.

*Provided as-is, without warranty or liability.*
