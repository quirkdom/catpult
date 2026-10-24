# Catpult Feature Matrix & Roadmap

Welcome to the **Catpult Feature Matrix**!

Catpult is an ephemeral, peer-to-peer transfer tool for sharing sensitive files (`.env`, certificates, tokens) and live byte streams directly between machines with **zero central servers**.

This document serves as both a **public feature tracker** and a **project management roadmap**. Whether you're using Catpult today or contributing code, this guide explains what's working right now, what's planned next, and what is intentionally kept out of scope to preserve Catpult's speed and security.

---

## Status Legend

| Status | Meaning |
| :--- | :--- |
| 🟢 **Available Now** | Fully implemented, tested, and shipped in the MVP (v0.1). |
| 🟡 **On the Cards** | Actively planned for upcoming phases (Phase 2 & Phase 3). |
| ⚪ **In Limbo / Under Evaluation** | Interesting possibilities, but pending upstream support or design review. |
| 🚫 **Not Feasible / Non-Goal** | Intentionally out of scope to protect simplicity, privacy, or architecture. |

---

## 1. Feature Matrix at a Glance

| Category | Feature | Status | Notes & Milestones |
| :--- | :--- | :---: | :--- |
| **File Sharing** | Ephemeral multi-file transfers | 🟢 Available | Send single or multiple files in one room (`catpult FILE...`). |
| | Late-joiner file delivery | 🟢 Available | Any peer joining while the room is open receives all files. |
| | File target directory (`--target`) | 🟢 Available | Receivers can route output to any directory (default: current dir). |
| | Manifest on stdout | 🟢 Available | Prints saved filenames to stdout; pipes cleanly to other tools. |
| **Stream Sharing** | Stdin streaming (`catpult -` / pipe) | 🟢 Available | Stream real-time data (`catpult -` or `cmd \| catpult`). |
| | 1:N live broadcast | 🟢 Available | Broadcasts stream to multiple connected peers simultaneously. |
| | Late-joiner stream playback | 🟢 Available | Replays in-memory buffer (up to 64 MiB) plus live tail. |
| | Raw stdout byte streaming | 🟢 Available | Composable with pipes (`catpult KEY \| cmd` or `> out`). |
| **Room Lifecycle** | Ephemeral host model | 🟢 Available | Sender is the host; no server remains after sender exits. |
| | Configurable TTL (`--expires`) | 🟢 Available | Auto-close after duration (`10m`, `1h`, default `30m`, `0` = until `^C`). |
| | Graceful shutdown & TCP drain | 🟢 Available | Sender drains open connections cleanly before exiting. |
| **Security & Safety** | WireGuard P2P encryption | 🟢 Available | Backed by Tailcat Go library with automatic NAT traversal & DERP relay. |
| | Path traversal protection | 🟢 Available | Strict sanitization; blocks `../`, absolute paths, and null bytes. |
| | Restricted permissions & cleanup | 🟢 Available | Writes files with `0600`; partial/interrupted files are removed. |
| | Strict protocol framing & caps | 🟢 Available | `EGAD` magic, 1 MiB frame cap, 100 MiB file cap, idle deadlines. |
| | Mode mismatch warnings | 🟢 Available | Warns on stderr (e.g. streaming to terminal) without blocking pipes. |
| **Peer & UX (Phase 2)** | Recipient display names | 🟡 On the cards | Optional client name sharing (`Alice's laptop`). |
| | Recipient connection tracking | 🟡 On the cards | Ephemeral host visibility into connected and active peers. |
| | Transfer progress & status UI | 🟡 On the cards | Richer stderr progress indicators during large transfers. |
| | Direct-only ("paranoid") mode | 🟡 On the cards | `--direct-only` flag to block transfers over DERP relays. |
| | Custom DERP configuration | 🟡 On the cards | `--derp` flag for enterprise/private relay infrastructure. |
| **Reach (Phase 3)** | Static browser receiver | 🟡 On the cards | In-browser receiver via Tailcat WASM (`https://host/#<key>`). |
| | Ephemeral replay buffer controls | 🟡 On the cards | Configurable buffer sizes for pastebin-style workflows. |
| | Package manager distributions | 🟡 On the cards | Homebrew, Scoop, and signed release archives. |
| **Exploratory** | Recipient-to-sender transfers | ⚪ In Limbo | Bidirectional rooms; needs protocol and security review. |
| | CLI auto-launch via URL handlers | ⚪ In Limbo | `catpult://<key>` OS integration; depends on desktop platforms. |
| **Non-Goals** | Central Catpult backend / DB | 🚫 Non-Goal | Violates zero-backend invariant. |
| | User accounts / passwords / orgs | 🚫 Non-Goal | Invites are secret bearer capabilities; no identity servers. |
| | Permanent cloud storage / vaults | 🚫 Non-Goal | Catpult is strictly ephemeral; data lives only in memory/sender. |
| | Mixing files & streams in one room | 🚫 Non-Goal | Separated to guarantee predictable Unix stdout redirection. |
| | Custom crypto / bespoke relays | 🚫 Non-Goal | Tailcat provides WireGuard encryption; no reinventing crypto. |
| | Background daemons / local IPC | 🚫 Non-Goal | Single-process sender keeps operational footprint minimal. |
| | Remote shell / execution / REPL | 🚫 Non-Goal | Security hazard; Catpult only handles opaque bytes. |

---

## 2. Deep Dive: What's Available Now (MVP)

The MVP is complete, robust, and designed around Unix simplicity and defensive coding:

### 🟢 Multi-File Mode
- **Simple command:** `catpult [--expires 10m] file1.env file2.pem`
- **Receiver:** `catpult KEY [--target ./destination]`
- **Late joiners:** Anyone holding the key can connect anytime before expiry and receive the full batch of files.
- **Clean output:** Filenames (and only filenames) are emitted to `stdout`, making scripts like `catpult KEY | xargs -n1 process` work reliably. All progress, status, and keys stay on `stderr`.

### 🟢 Streaming Mode
- **Simple command:** `catpult -` or `tail -f app.log | catpult`
- **Receiver:** `catpult KEY > output.log` or `catpult KEY | jq .`
- **1:N Broadcast:** Multiple receivers can connect concurrently; all receive the same stream.
- **Ring buffer catch-up:** Late joiners receive up to 64 MiB of buffered stream history before seamlessly switching to live data.
- **EOF semantics:** If standard input finishes, the room stays open for new peers to read the cached data until `--expires` or `Ctrl-C`.

### 🟢 Zero-Trust & Recipient Safety
- **No Catpult Servers:** The sender acts as the temporary room host. When the sender exits, the room ceases to exist.
- **Defensive File Writing:** Filenames from the sender are strictly validated. Catpult rejects directory traversals (`../`), absolute paths, hidden control characters, and symlink escapades.
- **Target Confinement:** Files are strictly constrained within `--target DIR` (defaulting to the current working directory) with safe `0600` permissions. If a transfer is interrupted, half-written files are deleted.
- **Safe Pipeline Contracts:** Streaming raw binary to a terminal or piping file lists produces clear warnings on `stderr` without breaking automated pipelines.

---

## 3. On the Cards Next

### 🟡 Phase 2: Visibility & Policy Controls
1. **Recipient Awareness:**
   - Give the sender insight into who is connected (`Alice's laptop`, `Anonymous`).
   - Exchange lightweight `HELLO` / `WELCOME` handshakes over the existing Tailcat link.
2. **Paranoid Mode (`--direct-only`):**
   - Guarantee that sensitive bytes only travel over direct peer-to-peer WireGuard connections, refusing to send payload data over DERP relay fallback nodes.
3. **Custom DERP Infrastructure (`--derp`):**
   - Allow enterprise users and private networks to point Catpult at their own self-hosted relay servers rather than public infrastructure.
4. **Enhanced UX:**
   - Clean terminal progress bars and transfer statistics on `stderr`.

### 🟡 Phase 3: Web Reach & Packaging
1. **Static In-Browser Receiver:**
   - Receive files in a modern web browser via Tailcat WASM (`https://catpult.app/#<invite>`).
   - The invite remains in the URL fragment (`#...`) so host servers never see the key.
2. **Configurable Ephemeral Buffering:**
   - Adjustable cache retention for extended pastebin-like workflows.
3. **Streamlined Installation:**
   - Automated packaging for Homebrew, Winget, Scoop, and official release signatures/checksums.

---

## 4. In Limbo / Under Evaluation

These ideas have been discussed, but need further technical investigation or upstream capabilities:

- ⚪ **Two-Way / Bidirectional Sharing:** Allowing a recipient to send a file back to the sender within the same room. Kept in limbo to avoid complicating the protocol and opening unintended attack surfaces on the host.
- ⚪ **OS URL Handler (`catpult://...`):** Clicking an invite link to auto-launch the CLI receiver. Requires platform-specific installation hooks and security consideration around arbitrary command triggers.
- ⚪ **Tailcat Path Query APIs:** Fine-grained detection of whether a live connection is relayed vs direct depends on upstream Tailcat library exposure.

---

## 5. Architectural Invariants (What We Will NOT Build)

To keep Catpult lightweight, auditable, and secure, certain features are **permanently out of scope**:

- 🚫 **No Catpult Cloud / Database / Auth:** There are no user accounts, passwords, or central databases. The room key is an ephemeral, secret bearer capability.
- 🚫 **No Permanent Vaults or Cloud Storage:** Catpult will never store files on third-party servers. Data is hosted by the sender's machine only for the duration of the room.
- 🚫 **No Mixed Payload Rooms:** A room is strictly for files OR strictly for a byte stream. Mixing both breaks Unix pipeline composability.
- 🚫 **No Custom Crypto:** Catpult relies exclusively on Tailcat's proven WireGuard implementation. We do not invent custom ciphers or hand-rolled relay protocols.
- 🚫 **No Background Daemons:** Catpult runs as a foreground process. No background services, IPC sockets, or hidden system footprint.
- 🚫 **No Remote Execution / REPL:** Catpult is a data transfer tool, not a remote shell. It will never execute received files or commands.
- 🚫 **No Delivery Receipts:** Deliveries are best-effort peer-to-peer. Catpult reports `sent` or `transfer completed`, never `delivered`, avoiding complex queue/ACK state machines.

---

*Suggestions or questions? Open an issue or check out [AGENTS.md](../AGENTS.md) for build and testing guidelines.*
