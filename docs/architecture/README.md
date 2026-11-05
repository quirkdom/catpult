# Architecture

These documents describe the **current implementation** and are the source of truth for architecture. Update the relevant page in the same change whenever code changes alter behavior, responsibilities, data flow, protocol, or dependencies. Keep details tied to code paths and avoid duplicating the architecture in other docs.

- [System overview](system.md) — process shape and package boundaries.
- [Sender modes](sender.md) — finite files, watch files, and stdin stream.
- [Receiver modes](receiver.md) — connection, mode dispatch, file safety, and output.
- [Wire protocol](protocol.md) — frames, messages, limits, and mode semantics.
- [Tailcat transport](transport.md) — network boundary and shutdown behavior.

For exact CLI syntax see [the CLI contract](../Catpult%20CLI%20MVP%20Spec.md). For build and verification commands see [AGENTS.md](../../AGENTS.md).
