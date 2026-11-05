# Auth Options & Threat Model Exploration

This document captures the analysis, threat model, cryptographic baseline, and architectural exploration for adding authentication options to Catpult beyond the default Tailcat bearer address.

---

## 1. Context & Motivation

* **Target Persona (ICP):** A developer who understands the importance of security but values speed and simplicity. They want a frictionless file-transfer tool that will not inadvertently expose their secrets.
* **The Problem:** The Tailcat address (`tc...`) is a **bearer capability**. Anyone in possession of the address can connect to the sender's room and receive the advertised payload.
* **The Core Vulnerability:** The bearer link is vulnerable to interception via:
  * Accidental paste into a public or team-wide channel (e.g. `#general`).
  * Chat history retention (Slack, Discord, Microsoft Teams).
  * Malicious bots or webhooks with channel read permissions.
  * Accidental display on a shared Zoom/Google Meet screen.
  * Local clipboard managers.

---

## 2. Cryptographic Baseline & Collision Resistance

Before evaluating additional authentication mechanisms, it is important to understand the baseline security provided by Tailcat.

### What is inside a `tc...` address?
A Tailcat address encodes:
1. **Server Node Public Key:** 32 bytes (256-bit Curve25519)
2. **Server Disco Public Key:** 32 bytes (256-bit Curve25519)
3. **Noise Pre-Shared Key (PSK):** 32 bytes (256-bit symmetric key, enabled by default in Tailcat)
4. **DERP Relay Region ID:** 2 bytes

### Can a `tc...` key be guessed or forged?
**No. It is mathematically impossible.**
* The address space has at least $2^{256} \approx 1.15 \times 10^{77}$ combinations.
* By the **Birthday Paradox**, achieving a 1-in-a-billion chance of a single collision anywhere globally would require generating over $6 \times 10^{34}$ keys.
* Even if every human on Earth ran `catpult` a billion times per second for 10 billion years, the probability of an address collision remains effectively zero.
* Generating a "valid-looking" address does not allow an attacker to connect: the client must complete a Diffie-Hellman handshake matching the private key held in the sender's memory, as well as the 256-bit PSK.

### Key Lifecycle & Ephemerality
* In Catpult, every invocation starts an ephemeral server with a newly generated private key and PSK in RAM.
* **Keys are never persisted to disk.**
* Once the process terminates (`^C`, TTL expiration, or single-use exit), the keys are purged from memory.
* An expired or killed room key is **permanently dead** and cannot be reconnected to under any circumstance.

---

## 3. Analysis of Proposed Auth Mechanisms

### A. Short Authentication String (SAS / Emoji Match)

* **Concept:** Both sides display a 3-emoji or 4-digit code derived from the session transcript (e.g., `HKDF(transcript, "catpult-sas")`).
* **Multi-Peer (Standup) Scenario:** 
  * Deriving pairwise SAS per peer creates confusion in a multi-client room (the sender would see different codes for each teammate).
  * Instead, derive a **Room-Level Safety Fingerprint** from the room's public key and PSK:
    $$\text{Room SAS} = \text{HKDF-SHA256}(\text{RoomPublicKey} \parallel \text{PSK}, \text{"catpult-room-sas"})$$
  * In a team call, the sender announces: *"Standup code is [ 🍕  🚀  ☕ ]"*. All teammates verify the match.
* **Critical Security Insight:**
  * **SAS proves channel integrity, NOT identity.**
  * It guarantees that both parties share the exact same key material and that no relay or proxy has manipulated the connection.
  * **However, it does not stop an attacker with a stolen link.** An attacker holding the stolen `tc...` key will compute and display the exact same SAS emoji. Unless two humans communicate out-of-band to confirm who is connecting, SAS provides zero protection against bearer token theft.
* **Verdict:** Excellent for "Paranoid / Verified" mode during live voice calls or pair programming; ineffective as an autonomous gate against stolen links.

---

### B. Passphrase / Shared Secret (`--pass` / `CATPULT_SECRET`)

* **Concept:** The sender and recipient share a secondary secret or team environment variable.
* **Human Behavior Risk:** Users often paste both the link and password into the same Slack message (`"Here's the link: tc... pass is 1234"`), neutralizing the second factor.
* **Architectural Insight — Redundant Encryption vs. WireGuard Gate:**
  * **Do NOT encrypt frames inside WireGuard:** Once the WireGuard tunnel is established, it provides ChaCha20-Poly1305 authenticated encryption with perfect forward secrecy. Re-encrypting frames at the application layer wastes CPU and adds code complexity with zero cryptographic benefit.
  * **Optimal Placement: WireGuard PSK Blending:**
    $$\text{Tunnel PSK} = \text{HKDF-SHA256}(\text{RandomPSK}, \text{Passphrase})$$
  * **Defense-in-Depth Advantage:**
    * If the recipient lacks the passphrase, the **WireGuard handshake fails immediately**.
    * No TCP connection is accepted.
    * No application framing or parsing code is executed.
    * This completely eliminates the attack surface for remote memory corruption, parser exploits, or unauthenticated denial-of-service.

---

### C. Self-Identified Interactive Approval ("Knock-Knock")

* **Concept:** Recipient automatically sends terminal metadata (`$USER@$(hostname)`) upon connecting. The sender's terminal prompts:
  ```text
  🔔 Incoming request from: alice (alice@macbook, darwin)
  Allow transfer? [Y/n]: 
  ```
* **Critical Security Insight:**
  * **Unauthenticated metadata is spoofable.** Anyone can run `USER=alice catpult KEY`. 
  * As defined in §15 of the Catpult Agent Implementation Spec: *"Do not treat a self-reported name as an authenticated identity."*
  * It cannot serve as an access-control boundary against a determined adversary.
* **Scriptability Bottleneck:**
  * Interactive prompts block automated scripts, CI/CD pipelines, and background processes unless bypassed via `--yes` or non-interactive TTY detection (`term.IsTerminal`).
* **Verdict:** Useful as a human confirmation heuristic, but must not be marketed as a cryptographic identity guarantee.

---

### D. Cryptographic Client Key Allowlisting (`--allow`)

* **Concept:** Tailcat natively supports client allowlisting at the WireGuard layer:
  ```go
  Server.AllowedClients []key.NodePublic
  ```
* **How it works:**
  * The recipient generates a persistent key (`tailcat genkey --client`).
  * The sender launches the room restricted to that key: `catpult --allow=nodekey:...`
  * During the initial discovery ping ("Meow"), Tailcat verifies the client's public key. Unauthorized clients are dropped before any tunnel is created.
* **Drawbacks for Ad-Hoc CLI Use:**
  * Inverts the push model to a pull model (recipient must transmit their public key first).
  * Introduces statefulness (storing client keys locally) which reduces portability for a simple CLI tool.
* **Verdict:** The gold standard for cryptographic identity. Ideal for Phase 2/3 (persistent team pairings, DNS TXT record lookups, or CI/CD pipelines).

---

## 4. Summary & Comparison

| Approach | Gating Layer | Protection Against Link Interception | Friction for Newbie Dev | Scriptable? |
| :--- | :--- | :--- | :--- | :--- |
| **Default (`tc...` key)** | Transport (WireGuard + PSK) | ❌ None (bearer token) | **Zero** | ✅ Yes |
| **PSK Passphrase Blending** | Transport (Noise Handshake) | ✅ Full (if shared out-of-band) | Low (optional env var or flag) | ✅ Yes |
| **Room SAS Match** | Visual verification | ⚠️ Channel integrity only (not identity) | Medium (requires vocal/visual check) | ❌ No |
| **Knock Metadata Prompt** | Application (TTY prompt) | ⚠️ Heuristic only (spoofable `$USER`) | Low for sender, zero for recipient | ⚠️ Requires `--yes` |
| **Client Key Allowlist** | Transport (`AllowedClients`) | ✅ Full cryptographic proof | High (pre-shared public keys) | ✅ Yes |

---

## 5. Architectural Recommendations for Catpult

1. **Keep the Base Tool Stateless & Frictionless:**
   The default `catpult FILE` experience should remain pure bearer capability—the fastest, zero-config way to send files.
2. **Implement Passphrase via PSK Blending:**
   When adding password support (`--pass` or `CATPULT_SECRET`), blend it directly into Tailcat's Noise PSK so that unauthorized peers are dropped at the packet filter / WireGuard layer before reaching any Catpult code.
3. **Reserve Cryptographic Client Keys for Named/Persistent Workflows:**
   When Catpult expands to support DNS TXT records or team contact directories, leverage Tailcat's native `AllowedClients` rather than inventing a custom identity protocol.
