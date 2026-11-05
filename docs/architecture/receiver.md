# Receiver modes

`runReceiver` creates a Tailcat client, dials the invite's Catpult port with a 30-second setup timeout, then passes the resulting `net.Conn` to `room.Receive`. The first Catpult frame selects the receiver path. The receiver does not consume stdin.

## Files

For `ROOM_INFO(file)`, `receiveFiles` creates the target directory (mode `0700`) and opens it as an `os.Root`. Each file header is validated, then written with mode `0600` through the rooted directory. Parent traversal, absolute paths, NUL, overlong names, and escapes are rejected. Chunks may not exceed the declared size; `FILE_END` is accepted only at exact size. Partial files are removed on errors.

On successful completion, the filename is printed to stdout, one per line. Status and mode warnings go to stderr. A clean EOF after a completed finite batch ends successfully; a mid-frame EOF is an error.

## Watch files

`ROOM_INFO(file-watch)` uses the same rooted writer and size checks, but the connection remains open. A new file header during a partial file supersedes that partial, which is removed before the replacement is written. After each complete version, the initial version prints its filename to stdout; later versions are reported as updates on stderr. The explicit initial-batch marker ends the initial phase; only then is idle waiting unbounded. EOF between versions means the room closed; EOF mid-file is an error.

## Stream

For `ROOM_INFO(stream)`, `receiveStream` writes every `STREAM_CHUNK` payload raw to stdout. This keeps pipelines composable (`catpult KEY > output`). A `STREAM_END` is reported as room closure; a clean TCP EOF also finishes successfully. Stream bytes are never parsed or staged to disk by Catpult.

## Connection and error handling

The initial handshake has a 30-second read deadline. File frames use a 60-second deadline; stream reads and watch idle periods clear it because indefinite pauses are valid. Local interrupt closes both the connection and Tailcat client to unblock processing. Protocol errors, malformed headers, unsafe paths, disk failures, and unexpected message types terminate that recipient session; the sender owns its independent room lifecycle.

See [protocol](protocol.md) for exact frame semantics and [sender](sender.md) for the corresponding send behavior.
