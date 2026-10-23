package room

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"catpult/internal/protocol"
)

// ErrRoomClosed is returned when the room ended (sender exited or
// TTL expired) rather than the transfer completing normally.
var ErrRoomClosed = errors.New("room closed")

// Receive reads the room protocol from conn and materializes the payload.
// stdout carries only payload output (filenames in file mode, raw bytes in
// stream mode); status and mismatch warnings go to warnf (stderr).
// isStdoutTTY describes the receiver's stdout context for mismatch warnings.
func Receive(conn net.Conn, targetDir string, stdout io.Writer, warnf func(string, ...any), isStdoutTTY bool) error {
	if err := conn.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	fr, err := protocol.ReadFrame(conn, protocol.MaxHeader)
	if err != nil {
		return fmt.Errorf("reading ROOM_INFO: %w", err)
	}
	if fr.Type != protocol.MsgRoomInfo {
		return fmt.Errorf("protocol: expected ROOM_INFO first, got 0x%02x", byte(fr.Type))
	}
	pt, err := protocol.DecodeRoomInfo(fr.Payload)
	if err != nil {
		return err
	}
	switch pt {
	case protocol.PayloadFile:
		if !isStdoutTTY {
			warnf("note: sender is sharing files; stdout lists received filenames, not file contents\n")
		}
		return receiveFiles(conn, targetDir, stdout)
	case protocol.PayloadStream:
		if isStdoutTTY {
			warnf("note: sender is streaming raw bytes; writing them to the terminal\n")
		}
		return receiveStream(conn, stdout)
	default:
		return fmt.Errorf("protocol: unknown payload type %v", pt)
	}
}

func receiveFiles(conn net.Conn, targetDir string, stdout io.Writer) error {
	if err := EnsureTargetDir(targetDir); err != nil {
		return err
	}
	var received int
	for {
		if err := conn.SetReadDeadline(time.Now().Add(frameTimeout)); err != nil {
			if received > 0 && isEOF(err) {
				return nil // finite transfer completed; sender closed
			}
			return err
		}
		fr, err := protocol.ReadFrame(conn, protocol.MaxFrame)
		if err != nil {
			if received > 0 && isEOF(err) {
				return nil // finite transfer completed; sender closed
			}
			return fmt.Errorf("receiving: %w", err)
		}
		switch fr.Type {
		case protocol.MsgFileHeader:
			name, size, err := protocol.DecodeFileHeader(fr.Payload)
			if err != nil {
				return err
			}
			if err := receiveOneFile(conn, targetDir, name, size); err != nil {
				return err
			}
			fmt.Fprintln(stdout, name)
			received++
		case protocol.MsgStreamChunk, protocol.MsgStreamEnd,
			protocol.MsgFileChunk, protocol.MsgFileEnd, protocol.MsgRoomInfo:
			return fmt.Errorf("protocol: unexpected message 0x%02x in file mode", byte(fr.Type))
		}
	}
}

func receiveOneFile(conn net.Conn, targetDir, name string, size uint64) error {
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		return fmt.Errorf("opening target dir: %w", err)
	}
	defer root.Close()
	dest, err := ValidateFilename(name, targetDir)
	if err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dest, err)
	}
	written := uint64(0)
	done := false
	defer func() {
		_ = f.Close()
		if !done {
			_ = os.Remove(dest) // no partial files on failure
		}
	}()
	for written < size {
		if err := conn.SetReadDeadline(time.Now().Add(frameTimeout)); err != nil {
			return err
		}
		fr, err := protocol.ReadFrame(conn, protocol.MaxFrame)
		if err != nil {
			return fmt.Errorf("receiving %s: %w", name, err)
		}
		if fr.Type != protocol.MsgFileChunk {
			return fmt.Errorf("protocol: expected file data for %s, got 0x%02x", name, byte(fr.Type))
		}
		if uint64(len(fr.Payload)) > size-written {
			return fmt.Errorf("protocol: %s overflow: %d bytes beyond declared size %d", name, uint64(len(fr.Payload))-(size-written), size)
		}
		if _, err := f.Write(fr.Payload); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
		written += uint64(len(fr.Payload))
	}
	// Expect FILE_END marker.
	if err := conn.SetReadDeadline(time.Now().Add(frameTimeout)); err != nil {
		return err
	}
	fr, err := protocol.ReadFrame(conn, protocol.MaxFrame)
	if err != nil {
		return fmt.Errorf("receiving end of %s: %w", name, err)
	}
	if fr.Type != protocol.MsgFileEnd {
		return fmt.Errorf("protocol: expected end of %s, got 0x%02x", name, byte(fr.Type))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dest, err)
	}
	done = true
	return nil
}

func receiveStream(conn net.Conn, stdout io.Writer) error {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(frameTimeout)); err != nil {
			if isEOF(err) {
				return nil
			}
			return err
		}
		fr, err := protocol.ReadFrame(conn, protocol.MaxFrame)
		if err != nil {
			if isEOF(err) {
				return nil
			}
			return fmt.Errorf("receiving stream: %w", err)
		}
		switch fr.Type {
		case protocol.MsgStreamChunk:
			if _, err := stdout.Write(fr.Payload); err != nil {
				return fmt.Errorf("writing stdout: %w", err)
			}
		case protocol.MsgStreamEnd:
			return ErrRoomClosed
		default:
			return fmt.Errorf("protocol: unexpected message 0x%02x in stream mode", byte(fr.Type))
		}
	}
}

func isEOF(err error) bool {
	for err != nil {
		// net.Pipe (used in tests) reports a closed peer as
		// io.ErrClosedPipe rather than io.EOF; real TCP (Tailcat)
		// reports EOF. Both mean "clean end of transfer" here.
		if err == io.EOF || err == io.ErrUnexpectedEOF || err == io.ErrClosedPipe {
			return true
		}
		if strings.Contains(err.Error(), "closed pipe") {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
