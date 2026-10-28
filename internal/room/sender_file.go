package room

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"catpult/internal/protocol"
)

// Timeouts for transfer I/O.
const (
	handshakeTimeout = 30 * time.Second
	frameTimeout     = 60 * time.Second
)

// errFileChanged is an internal sentinel (watch.go): the watched file
// changed mid-send and the transfer aborted at a chunk boundary — no
// FILE_END was written, so the receiver keeps the partial until the next
// FILE_HEADER supersedes it.
var errFileChanged = errors.New("file changed during send")

// ServeFileConn serves the full file payload to one recipient connection.
// Files are re-read from disk per joiner so late joiners get everything.
// Best effort: errors close this connection only, never the room.
func ServeFileConn(conn net.Conn, files []string, onFile func(name string, size int64)) error {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgRoomInfo, protocol.EncodeRoomInfo(protocol.PayloadFile)); err != nil {
		return err
	}
	for _, path := range files {
		if err := sendOneFile(conn, path, onFile); err != nil {
			return err
		}
	}
	// Finite transfer complete: half-close the write side so the
	// receiver sees EOF. Full Close happens via defer; DrainTCP at
	// shutdown flushes the FIN.
	return nil
}

func sendOneFile(conn net.Conn, path string, onFile func(name string, size int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return fmt.Errorf("%s is not a regular file", path)
	}
	// Finite path: no change hook, so the write behavior is byte-for-byte
	// identical to the pre-watch implementation.
	return writeFile(conn, filepath.Base(path), path, f, fi.Size(), nil, onFile)
}

// writeFile streams an open regular file to conn: FILE_HEADER, then
// ChunkSize chunks, then FILE_END. Shared by the finite and watch senders
// so the size-consistency checks live in one place. changed, when non-nil,
// is checked before each chunk read and before FILE_END; if it ever
// returns true the transfer aborts with errFileChanged (no FILE_END) so a
// watch sender can restart with a fresh header. f is always closed.
// onFile, when non-nil, is called once with name/size before the header
// write (stderr progress hooks).
func writeFile(conn net.Conn, name, path string, f *os.File, size int64, changed func() bool, onFile func(name string, size int64)) error {
	defer f.Close()
	hdr, err := protocol.EncodeFileHeader(name, uint64(size))
	if err != nil {
		return err
	}
	if onFile != nil {
		onFile(name, size)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(frameTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgFileHeader, hdr); err != nil {
		return fmt.Errorf("sending header for %s: %w", name, err)
	}
	buf := make([]byte, protocol.ChunkSize)
	var sent int64
	for sent < size {
		if changed != nil && changed() {
			return errFileChanged
		}
		if err := conn.SetWriteDeadline(time.Now().Add(frameTimeout)); err != nil {
			return err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			if werr := protocol.WriteFrame(conn, protocol.MsgFileChunk, buf[:n]); werr != nil {
				return fmt.Errorf("sending %s: %w", name, werr)
			}
			sent += int64(n)
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			// Watch mode: a mid-send read error may be the file being
			// rewritten under us (truncate/rename races the poller's
			// version bump). If the watcher agrees, supersede instead of
			// failing fatal and severing the recipient connection.
			if changed != nil && changed() {
				return errFileChanged
			}
			return fmt.Errorf("reading %s: %w", path, rerr)
		}
	}
	if sent != size {
		// Watch mode: a short read on an open regular file proves the
		// file was truncated/rewritten concurrently — the poller may not
		// have bumped the version yet, so supersede immediately rather
		// than failing fatal and severing the recipient connection.
		if changed != nil {
			return errFileChanged
		}
		return fmt.Errorf("%s: size changed during send (%d != %d)", path, sent, size)
	}
	if changed != nil && changed() {
		return errFileChanged
	}
	if err := conn.SetWriteDeadline(time.Now().Add(frameTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgFileEnd, nil); err != nil {
		return fmt.Errorf("sending end for %s: %w", name, err)
	}
	return nil
}
