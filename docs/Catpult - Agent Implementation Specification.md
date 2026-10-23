# Catpult

## 1. Overview

Catpult is an ephemeral, peer-to-peer tool for securely broadcasting arbitrary sensitive artifacts — especially `.env` files and development credentials — from one sender to one or more recipients.

The primary use case is replacing insecure/convenient workflows such as:

- sending `.env` files through Slack/Teams/DMs
- putting secrets into paste services
- creating temporary secret-sharing links through third-party services
- committing secrets to VCS

The core design principle is:

> **Catpult should not require a Catpult server.**

The sender creates an ephemeral room locally. Recipients join using a bearer invite shared out-of-band. Tailcat provides the encrypted network connection. Catpult provides the room and artifact protocol.

The MVP is CLI-only.

---

# 2. Design principles

1. **No Catpult backend**
   - No database.
   - No API server.
   - No authentication service.
   - No persistent Catpult infrastructure.
   - The sender is the room host.

2. **Use Tailcat for transport**
   - Depend on the Tailcat Go library rather than invoking the Tailcat CLI.
   - Do not implement replacement networking, NAT traversal, WireGuard, or relay functionality.
   - Do not implement another encryption layer over Tailcat unless a concrete security requirement demands it.

3. **Ephemeral by default**
   - A room exists only while the sender's process is running and its invite is valid.
   - MVP does not persist artifacts or room state beyond the sender process.

4. **Out-of-band invitation**
   - The room invite/key is generated locally.
   - The user is responsible for distributing it through an external channel.

5. **One-way application semantics**
   - One sender broadcasts artifacts.
   - Recipients receive.
   - Recipients do not send artifacts in MVP.
   - Tailcat itself is bidirectional; the application protocol should nevertheless expose only the minimum functionality required.

6. **Security through simplicity**
   - Prefer Tailcat's existing cryptographic guarantees over custom cryptography.
   - Keep the application protocol small and auditable.
   - Never expose arbitrary filesystem or command-execution functionality through the protocol.

---

# 3. Phased roadmap

## Phase 1 — MVP

CLI-only. Single-process sender model — there are no `create` / `join` / `send`
subcommands and no daemon/local IPC. The sender process is the room host; a
second sender-side `send` process does not exist.

Features:

- sender: `catpult [--expires DURATION] FILE...` or stream mode
  (`catpult [--expires DURATION] -`, equivalently `producer | catpult [--expires DURATION]`)
- generate invite, print it, wait for recipients
- every recipient that joins receives **all files** (file mode) or the same
  byte stream (stream mode, buffered + live bytes)
- receiver: `catpult KEY [--target DIR]`
- file mode materializes files into target dir (default cwd) and prints
  received filenames to stdout, one per line
- stream mode writes raw received bytes to stdout
  (`catpult KEY | command`, `catpult KEY > output`)
- stdout is payload output; stderr is human status/progress/errors
- receiver warns on stderr on mode/context mismatch (e.g. file sender +
  piped receiver, stream sender + terminal receiver); never block
  non-interactive pipelines on confirmation
- room expires when:
  - configured invite/room TTL expires, OR
  - sender exits (^C)
- best-effort delivery
- no ACKs
- no recipient identity/tracking
- no browser
- no custom DERP
- no direct-only/paranoid mode
- no recipient → sender artifact transfer

The MVP should be intentionally small.

## Phase 2 — Improvements

Add:

- recipient tracking
- optional recipient display name
- application-level HELLO/WELCOME
- delivery/path status
- direct-only ("paranoid") mode
- detect whether recipient connection is direct or DERP-relayed
- custom DERP configuration
- better lifecycle/status UX

## Phase 3 — Pro

Potentially add:

- ephemeral persistence/message queue semantics
- recipients joining after artifacts were sent
- pastebin-like UX
- static browser receiver
- Tailcat WASM
- browser → CLI and CLI → browser interoperability
- recipient promotion / sender capabilities
- richer room protocol

Phase 3 is explicitly out of scope for the initial implementation.

---

# 4. MVP CLI

Authoritative CLI contract: `docs/Catpult CLI MVP Spec.md`. This section
summarizes it; on conflict the CLI spec wins.

Sender (files):

```bash
catpult [--expires DURATION] FILE...
catpult --expires 10m foo.txt bar.txt
```

Sender (stdin stream):

```bash
catpult [--expires DURATION] -
producer | catpult [--expires DURATION]
```

- `-` means read the payload from stdin, exactly like `cat`.
- File arguments and stdin are mutually exclusive.
  `producer | catpult FILE` is invalid, as is combining `-` with files.
- The command starts an ephemeral Tailcat server, prints the invite/key,
  waits for recipients, and keeps accepting them until `--expires` or `^C`.
- Stream-mode bytes are buffered in server cache; recipients may join at any
  time and receive buffered bytes followed by subsequent bytes. Every
  recipient receives the same byte stream.

Receiver:

```bash
catpult KEY [--target DIR]
```

The CLI distinguishes sender from receiver by argument shape: a single
`KEY` (a parseable Tailcat address) with no file operands means receiver;
file operands / `-` / piped stdin mean sender. Ambiguous input is an error.

Example sender output (stderr carries status; stdout stays free for piping):

```text
Catpult room created.

Expires: 10 minutes

Invite:
tc....

Share this invite with recipients out of band.

Waiting for recipients...
```

The invite should be treated as a secret bearer capability.

Do not describe it to users as an encryption key unless necessary.

---

# 5. Joining (receiving)

Recipient:

```bash
catpult KEY [--target DIR]
```

The CLI:

1. Parses the invite (KEY = Tailcat address).
2. Creates a Tailcat client.
3. Connects to the sender.
4. Blocks until the room expires/closes, `^C`, or a finite transfer completes.
5. Writes received artifacts according to the receiving policy:
   - file mode → files materialized under `--target DIR` (default cwd);
     filenames emitted to stdout, one per line, after successful receipt
     (same manifest when stdout is piped/redirected).
   - stream mode → raw received bytes written directly to stdout, so
     `catpult KEY | command` and `catpult KEY > output` compose.
6. Never consumes stdin: `something | catpult KEY` is invalid.

MVP does not require recipient authentication or identity. There is no
`--name` flag in MVP (dropped; was Phase 2 forward-compat).

---

# 6. Sending artifacts

There is no separate `send` command. The sender's file operands (or stdin)
declared at startup are the payload for the room's lifetime:

```bash
catpult --expires 10m .env
catpult --expires 10m .env cert.pem config.json
catpult --expires 10m -
cat .env | catpult --expires 10m
```

Do not add secret-management semantics. Catpult transfers opaque byte sequences.

The application should not interpret `.env`, JSON, PEM, certificates, etc.

File and stream payloads cannot be mixed in one room.

---

# 7. MVP delivery semantics

Delivery is **best effort**.

There are deliberately no:

- delivery ACKs
- read receipts
- persistent queues
- retransmission protocol beyond what Tailcat/TCP provides
- recipient status guarantees

If a recipient joins at any time while the room is alive, the sender serves it
the full payload: all files (file mode) or buffered bytes followed by live
bytes (stream mode).

If the recipient disconnects during transfer, the artifact may be lost.

The sender should report transport/application errors when they occur.

Example (stderr; stdout stays payload-clean):

```text
Sending .env (2.4 KB)...

2 recipients connected.
✓ sent to connected recipients.
```

Do not claim "delivered" because the MVP has no application-level ACK.

Prefer language such as "sent" or "transfer completed" only when the local write/transport operation has actually completed.

---

# 8. Room lifecycle

A room terminates on the earliest of:

```text
sender exits (^C)
OR
TTL expires
```

(For stream mode, stdin EOF does NOT close the room in MVP — struck through
in the CLI spec. The room stays open for late joiners until ^C/expiry.)

A receiver exits on the earliest of:

```text
^C
OR
room closure/expiry
OR
completion of a finite transfer
```

When the room terminates:

1. Stop accepting new Tailcat connections.
2. Close existing connections.
3. Destroy the Tailcat server.
4. Release all room state.
5. Do not retain artifacts.

MVP does not need secure deletion guarantees.

Do not claim that deleting temporary files guarantees physical erasure from SSDs/filesystems.

Ideally, avoid creating persistent copies altogether.

---

# 9. Artifact handling

MVP should avoid unnecessary staging.

Where possible:

```text
source file
    ↓
read/stream
    ↓
Tailcat connection
    ↓
recipient
```

Do not upload artifacts to any Catpult-controlled server.

Do not send artifacts through an application-controlled relay.

Do not store artifacts remotely.

File-mode payloads are retained by the sender process for the room lifetime
(re-read from disk or held in memory) so late joiners receive all files.
Stream-mode bytes are buffered in server cache (memory) so late joiners
receive buffered + subsequent bytes. This is bounded ephemeral in-process
retention for the current room only — not a vault, queue, or history store.

If temporary local storage is required, use restrictive permissions and clean it up on termination.

---

# 10. Application protocol

Tailcat provides the network transport.

Catpult should implement a minimal application protocol above it.

For MVP, the conceptual messages are:

```text
HOST → RECEIVER

ROOM_INFO {
    protocol_version
    payload_type  # file | stream
}

HOST → RECEIVER (file mode, repeated per file)

ARTIFACT {
    artifact_id
    filename
    size
    payload
}

HOST → RECEIVER (stream mode)

STREAM_CHUNK {
    offset
    payload
}
STREAM_END {
}
```

The exact wire encoding is an implementation choice, but it should be:

- self-identifying: every frame starts with a magic prefix, so a receiver can
  tell Catpult traffic from another service answering on the same port
- versioned
- length-delimited
- unambiguous
- resistant to malformed input
- streamable
- independent of Tailcat's own wire protocol

The magic prefix and the version byte are separate on purpose: the magic names
the product, the version names the wire revision. That keeps "not a Catpult
peer" distinguishable from "Catpult, but a version I do not speak".

Do not expose arbitrary RPC or command execution.

---

# 11. Application-layer security

Do not implement custom encryption over Tailcat merely for defense in depth.

Tailcat already provides the encrypted network transport.

Catpult is responsible for application-level safety.

At minimum:

- validate message framing
- enforce maximum message/artifact sizes
- validate filenames
- prevent path traversal
- never allow absolute destination paths from sender-controlled metadata
- do not allow `../`
- do not interpret received filenames as commands
- protect against integer overflow when parsing lengths
- enforce reasonable timeouts
- handle malformed/malicious clients gracefully
- do not expose host filesystem contents
- do not expose arbitrary host network services

A recipient must never be able to make the host execute a command.

A sender must never be able to make a recipient write outside the intended receiving directory.

A frame magic prefix identifies Catpult traffic. It does not authenticate the
peer: anything that can reach the room port inside the Tailcat tunnel can send
it. The invite is the access control.

---

# 12. Tailcat dependency

Use the Tailcat Go library directly.

Do NOT:

```text
exec("tailcat ...")
```

and parse CLI output.

Catpult should have a thin internal abstraction over Tailcat so that the rest of the application does not depend heavily on Tailcat implementation details.

Conceptually:

```go
type TransportServer interface {
    Accept() ...
    Close() error
}

type TransportClient interface {
    Connect(...) ...
    Close() error
}
```

The exact interfaces should follow the actual Tailcat Go API rather than inventing an unnecessary abstraction layer.

Pin a known Tailcat version/commit.

Tailcat's API and wire format are currently experimental and do not provide long-term compatibility guarantees. Treat Tailcat upgrades as explicit dependency upgrades.

---

# 13. Invite representation

The Tailcat address is itself a bearer capability and must be treated as secret.

The MVP may expose the Tailcat-generated invite directly.

Example:

```text
tc...
```

Future versions may wrap the Tailcat address in a Catpult-specific versioned envelope:

```text
sd1.<encoded-payload>
```

containing things such as:

```text
version
expiry
room metadata
Tailcat address
flags
```

Do not implement custom cryptographic obfuscation merely to hide the Tailcat address.

Encoding/obfuscation is not security.

The URL/static-page representation is Phase 3.

---

# 14. Phase 2: recipient tracking

Because Tailcat connections are bidirectional, recipients can send application-level metadata back to the sender.

Introduce:

```text
RECEIVER → HOST

HELLO {
    protocol_version
    display_name?
}
```

The sender maintains ephemeral in-memory recipient state:

```text
Recipient {
    connection
    peer identity
    display name
    connection state
    path state
}
```

The display name is informational only.

Do not treat a self-reported name as an authenticated identity.

Where available through Tailcat, use the authenticated cryptographic peer identity as the stable session identifier.

Example UI:

```text
Recipients:

Alice's laptop    connected
Bob's laptop      connected
Anonymous          connected
```

No central identity provider is required.

---

# 15. Phase 2: paranoid/direct-only mode

Add:

```bash
catpult create --direct-only
```

Semantics:

> Never transmit artifact payload bytes over a DERP-relayed connection. Only recipients with an established direct path may receive artifacts.

A recipient may still establish/control a Tailcat connection through DERP.

For example:

```text
Recipient     Path       Artifact eligible
Alice         DIRECT     yes
Bob           DERP       no
Carol         DIRECT     yes
```

If Bob later obtains a direct path, he becomes eligible.

The application must distinguish:

```text
connection exists
```

from:

```text
connection has direct data path
```

Do not infer path type from latency.

Use Tailcat's actual path information/API.

The current Tailcat CLI exposes `ping --until-direct`; investigate the corresponding Go-library capability. If the library does not expose a stable path-state API, consider contributing the minimal required API upstream rather than reaching into Tailcat internals.

Important:

**DERP does not mean plaintext.**

Both direct and DERP-relayed Tailcat traffic is protected by the WireGuard tunnel.

`--direct-only` is an infrastructure/privacy/availability policy, not a stronger cryptographic mode.

---

# 16. Phase 2: custom DERP

Allow users to specify their own DERP infrastructure.

Example conceptual configuration:

```bash
catpult create --derp <custom-derp>
```

The exact configuration mechanism should follow Tailcat's supported APIs.

Use cases:

- enterprise users
- organizations that don't want to depend on public Tailcat DERP infrastructure
- private networks
- controlled infrastructure

If the user's own DERP is used, direct and relayed paths can be treated as equivalent with respect to infrastructure trust, while retaining the same WireGuard encryption.

Do not implement a custom relay protocol.

Use Tailcat's supported DERP implementation.

---

# 17. Phase 3: ephemeral persistence

MVP already has bounded in-room retention: file-mode senders serve all files
to late joiners, and stream-mode senders buffer bytes for late joiners, for
the life of the sender process. Phase 3 generalizes this toward pastebin-like
semantics (larger caches, replay controls) — still ephemeral, still hosted by
the sender, never Catpult cloud storage.

Potential future semantics:

```text
catpult --expires 30m .env
```

(The old `create` / `send` split is gone: the payload is declared at startup.
No second sender-side process, no daemon/IPC.)

The sender keeps artifacts available for the lifetime of the room.

A recipient joining after the send can receive previously published artifacts.

Conceptually:

```text
Room
 ├── artifact A
 ├── artifact B
 └── artifact C

Recipient joins
       ↓
receives available artifacts
```

This is deliberately **ephemeral persistence**, not a permanent secret vault.

The host remains the storage location.

No Catpult cloud storage is required.

---

# 18. Phase 3: static browser receiver

Eventually support:

```text
https://<static-site>/#<invite>
```

The invite must be stored in the URL fragment rather than query/path parameters.

Reason:

The fragment is processed by the browser and is not sent as part of the HTTP request to the static server.

The static site should therefore be able to remain completely ignorant of the room.

Architecture:

```text
Browser
  │
  │ GET /
  ▼
Static Catpult page

Browser reads:
location.hash
  │
  ▼
Tailcat WASM
  │
  ▼
Sender
```

Investigate Tailcat's existing WASM/browser implementation rather than implementing a separate browser transport.

Current browser limitations, including DERP-only operation, should be treated as Tailcat capabilities rather than solved in Catpult initially.

---

# 19. Trust model

A core architectural property is:

> **Catpult should not need to be trusted in order to use Catpult.**

The sender should be able to use the CLI without contacting Catpult infrastructure.

The eventual web client should be static and independently self-hostable.

Potential trust hierarchy:

```text
Source code
    ↓
reproducible build
    ↓
signed release artifact
    ↓
local CLI
    ↓
Tailcat
    ↓
recipient
```

Future releases should provide:

- public source code
- signed binaries/releases
- checksums
- reproducible builds where practical
- build provenance/attestation
- standard package-manager distribution where appropriate

Avoid requiring users to execute opaque remote installer scripts as the primary installation method.

---

# 20. No Catpult server as an architectural invariant

Do not introduce a Catpult-controlled backend merely to solve:

- room coordination
- recipient tracking
- invite validation
- message delivery
- persistence
- browser bootstrap

Those functions should first be investigated as possible application-layer functions over the existing Tailcat connection.

A future hosted service may be considered for convenience, but the core protocol must remain usable without it.

The static browser page is not a backend.

The sender is the room host.

Tailcat is the transport.

---

# 21. Explicit non-goals for MVP

Do NOT implement:

- accounts
- login
- teams
- organization management
- persistent rooms
- cloud storage
- secret vaults
- password management
- recipient authentication
- delivery receipts
- message history
- custom cryptography
- custom networking
- custom relay infrastructure
- DERP policy controls
- browser support
- automatic CLI launching from URLs
- recipient → sender transfer
- sender promotion
- arbitrary RPC
- command execution
- REPL
- multi-room management
- daemon / local IPC (rejected: single-process sender instead)
- mixing file and stream payloads in one room

The MVP should be a small, auditable CLI around Tailcat.

---

# 22. Suggested implementation order

### Step 1
Read the current Tailcat Go API and identify:

- server creation
- client creation
- address/invite generation
- connection acceptance
- connection establishment
- peer lifecycle
- graceful shutdown

### Step 2
Build sender + receiver with a raw test message (no protocol yet):

```text
sender:   catpult --expires 10m <test-file>
receiver: catpult <invite>
```

### Step 3
Implement the minimal versioned application protocol.

### Step 4
Implement artifact streaming.

### Step 5
Implement one-to-many broadcasting.

### Step 6
Implement TTL and graceful room shutdown.

### Step 7
Add security/resource limits and malformed-input handling.

### Step 8
Add integration tests using multiple local Tailcat peers.

### Step 9
Document the security/threat model.

Only after this is solid should Phase 2 begin.

---

# 23. MVP success criteria

The MVP is successful if:

1. Alice can run:

```bash
catpult --expires 10m .env
```

2. Alice can copy the generated invite through an arbitrary out-of-band channel.

3. Bob can run:

```bash
catpult <invite> --target ./inbox
```

4. Bob establishes a Tailcat connection without any Catpult server.

5. Bob receives the bytes: file mode materializes `./inbox/.env` and prints
   the filename to stdout; stream mode (`catpult <invite> > out`) writes raw
   bytes to stdout.

6. Alice can have multiple recipients simultaneously; every recipient that
   joins while the room is alive receives all files / the same stream.

7. Late joiners receive the full payload (files, or buffered + live stream bytes).

8. The artifact is not stored by Catpult infrastructure.

9. No application-level delivery guarantee is claimed.

10. Alice exiting the process destroys the room.

11. TTL expiration destroys the room.

12. The implementation contains no custom cryptographic protocol.

13. The CLI can operate with no network dependency on Catpult-owned infrastructure.

---

# 24. Security statement for the MVP

The implementation/documentation should be careful about claims.

It is reasonable to state that Catpult uses Tailcat's encrypted transport and that artifact payloads are transmitted through that transport.

Do **not** claim:

- "unhackable"
- "zero knowledge" without defining the claim
- "anonymous"
- "securely deleted"
- "guaranteed direct P2P"
- "no metadata leakage"

Tailcat's own security posture and experimental API status should be taken into account.

Catpult should maintain a separate application-level threat model and should not imply that Tailcat's underlying cryptography automatically makes the entire application secure.

---

# 25. Product philosophy

The intended experience should ultimately feel like:

> **"I need to get this secret to these people right now."**

Not:

> "I need to manage secrets."

The ideal future command remains something close to:

```bash
catpult .env
```

which creates a short-lived room, produces an invite, and lets the user broadcast the artifact without putting it into Slack, Git, cloud storage, or a permanent secret manager.

The product should remain deliberately ephemeral, simple, and infrastructure-independent.
