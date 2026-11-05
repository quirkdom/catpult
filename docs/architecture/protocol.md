# Catpult wire protocol

The application protocol runs over the `net.Conn` supplied by Tailcat. Every frame is:

```text
"EGAD" | version:u8 | type:u8 | payload_length:uvarint | payload
```

Current version is `1`. `EGAD` identifies Catpult traffic; it is not authentication. `ReadFrame` validates magic, version, known message type, length, and complete payload before a room handler sees a frame.

## Limits

- Maximum frame payload: 1 MiB.
- Maximum structured/header payload: 4 KiB.
- Maximum filename: 255 bytes.
- File and stream data are sent in 32 KiB chunks; file size is not capped by the protocol, so large files remain constant-memory at sender/receiver.
- Stream late-join history is capped at 64 MiB; oldest bytes are evicted.

## Message types

| Type | Value | Payload / use |
| --- | ---: | --- |
| `ROOM_INFO` | `0x01` | One byte: `file=0`, `stream=1`, `file-watch=2`. First on every connection. |
| `FILE_HEADER` | `0x02` | `uvarint(name length) + name + uvarint(file size)`. |
| `FILE_CHUNK` | `0x03` | Opaque bytes; only after a file header. |
| `FILE_END` | `0x04` | Empty payload; completes the current declared file. |
| `STREAM_CHUNK` | `0x05` | Opaque stream bytes. |
| `STREAM_END` | `0x06` | Empty payload; stream input ended or is being shut down. |
| `WATCH_INITIAL_END` | `0x07` | Empty payload; initial watch batch has completed. |

## Mode sequences

- Finite files: `ROOM_INFO(file)`, repeated `FILE_HEADER`, zero or more `FILE_CHUNK`, `FILE_END`; sender closes after the batch.
- Watch files: `ROOM_INFO(file-watch)`, initial file sequence, `WATCH_INITIAL_END`, then repeated file sequences until connection close. A new `FILE_HEADER` during an incomplete file replaces that partial file.
- Stream: `ROOM_INFO(stream)`, zero or more `STREAM_CHUNK`, optionally `STREAM_END`; TCP close can also terminate a stream during room shutdown.

Files and streams never mix within one room. Receivers reject message types that do not belong to the announced mode. In watch mode, EOF between files means room closure; finite-file EOF after at least one complete file means transfer completion.

## Compatibility

The version and payload types are strict. There is no negotiation or fallback: an unknown version, type, or payload type fails that connection. Adding a message or changing encoding requires updating `internal/protocol/`, its tests, the relevant room state machines, and this page together.
