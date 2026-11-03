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
// stream mode); status goes to infof (stderr) and mismatch warnings go to warnf (stderr).
// isStdoutTTY describes the receiver's stdout context for mismatch warnings.
// It returns the number of files received (in file mode) and any error.
func Receive(conn net.Conn, targetDir string, stdout io.Writer, warnf, infof func(string, ...any), isStdoutTTY bool) (int, error) {
	emitWarn := func(format string, args ...any) {
		if warnf != nil {
			warnf(format, args...)
		}
	}
	emitInfo := func(format string, args ...any) {
		if infof != nil {
			infof(format, args...)
		}
	}

	if err := conn.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return 0, err
	}
	fr, err := protocol.ReadFrame(conn, protocol.MaxHeader)
	if err != nil {
		return 0, fmt.Errorf("reading ROOM_INFO: %w", err)
	}
	if fr.Type != protocol.MsgRoomInfo {
		return 0, fmt.Errorf("protocol: expected ROOM_INFO first, got 0x%02x", byte(fr.Type))
	}
	pt, err := protocol.DecodeRoomInfo(fr.Payload)
	if err != nil {
		return 0, err
	}
	switch pt {
	case protocol.PayloadFile:
		if !isStdoutTTY {
			emitWarn("note: sender is sharing files; stdout lists received filenames, not file contents\n")
		}
		return receiveFiles(conn, targetDir, stdout, emitWarn, emitInfo, false)
	case protocol.PayloadFileWatch:
		if !isStdoutTTY {
			emitWarn("note: sender is sharing files in watch mode; stdout lists received filenames, not file contents\n")
		}
		return receiveFiles(conn, targetDir, stdout, emitWarn, emitInfo, true)
	case protocol.PayloadStream:
		if isStdoutTTY {
			emitWarn("note: sender is streaming raw bytes; writing them to the terminal\n")
		}
		return 0, receiveStream(conn, stdout)
	default:
		return 0, fmt.Errorf("protocol: unknown payload type %v", pt)
	}
}

// receiveFiles receives file payloads, in both finite (MVP) and watch
// flavors. In watch mode the connection stays open and a FILE_HEADER seen
// while a file is mid-transfer means "discard the partial and start over"
// (implicit in-flight supersede); while idle between versions no read
// deadline applies, but chunk reads carry the usual frame deadline, and
// EOF maps to ErrRoomClosed.
func receiveFiles(conn net.Conn, targetDir string, stdout io.Writer, warnf, infof func(string, ...any), watch bool) (int, error) {
	if err := EnsureTargetDir(targetDir); err != nil {
		return 0, err
	}
	if watch {
		// Clear the handshake deadline: idle time between watched-file
		// versions is normal, not an error condition.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return 0, err
		}
	}
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		return 0, fmt.Errorf("opening target dir: %w", err)
	}
	defer root.Close()

	var (
		received         int
		seenFiles        = make(map[string]bool)
		initialAnnounced bool
		curName          string
		curDest          string
		curSize          uint64
		written          uint64
		curFile          *os.File
	)
	// discard closes the in-progress file and removes its partial bytes.
	// Used both for the implicit-supersede path and error cleanup.
	discard := func() {
		if curFile != nil {
			_ = curFile.Close()
			_ = os.Remove(curDest) // no partial files on failure/supersede
			curFile = nil
		}
	}
	defer discard()

	for {
		// Read deadline policy: finite mode applies a per-frame deadline
		// unconditionally. Watch mode clears it while idle between file
		// versions (gaps are normal) and applies it while a file is
		// mid-transfer, so a stalled sender still aborts at frameTimeout.
		// Immediately after the initial burst of files lands in watch mode,
		// a short deadline detects that the sender has gone idle so we can
		// announce completion of the initial receive.
		deadline := time.Now().Add(frameTimeout)
		if watch && curFile == nil {
			if !initialAnnounced && len(seenFiles) > 0 {
				deadline = time.Now().Add(50 * time.Millisecond)
			} else {
				deadline = time.Time{}
			}
		}
		// A deadline-set failure usually means the connection is already
		// dead; classify it (and the read) through the same EOF logic
		// below rather than failing outright.
		err := conn.SetReadDeadline(deadline)
		var fr protocol.Frame
		if err == nil {
			fr, err = protocol.ReadFrame(conn, protocol.MaxFrame)
		}
		if err != nil {
			if watch && curFile == nil && !initialAnnounced && len(seenFiles) > 0 {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					infof("received %d file(s). waiting for updates...\n", len(seenFiles))
					initialAnnounced = true
					_ = conn.SetReadDeadline(time.Time{})
					continue
				}
			}
			// ErrUnexpectedEOF means a frame was torn mid-read: that is
			// truncation, never a clean end, even between files.
			if isEOF(err) && !errors.Is(err, io.ErrUnexpectedEOF) {
				if curFile != nil {
					return 0, fmt.Errorf("receiving %s: connection closed mid-transfer: %w", curName, io.ErrUnexpectedEOF)
				}
				if watch {
					return received, ErrRoomClosed // room ended while staying open
				}
				if received > 0 {
					return received, nil // finite transfer completed; sender closed
				}
			}
			return 0, fmt.Errorf("receiving: %w", err)
		}
		switch fr.Type {
		case protocol.MsgFileHeader:
			// A header between files opens the next file; a header
			// mid-file supersedes the partial (watch fuzz/supersede).
			discard()
			name, size, err := protocol.DecodeFileHeader(fr.Payload)
			if err != nil {
				return 0, err
			}
			dest, err := ValidateFilename(name, targetDir)
			if err != nil {
				return 0, err
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return 0, fmt.Errorf("creating %s: %w", dest, err)
			}
			curName, curDest, curSize, written, curFile = name, dest, size, 0, f
		case protocol.MsgFileChunk:
			if curFile == nil {
				return 0, fmt.Errorf("protocol: file data before any FILE_HEADER")
			}
			if uint64(len(fr.Payload)) > curSize-written {
				return 0, fmt.Errorf("protocol: %s overflow: %d bytes beyond declared size %d", curName, uint64(len(fr.Payload))-(curSize-written), curSize)
			}
			if _, err := curFile.Write(fr.Payload); err != nil {
				return 0, fmt.Errorf("writing %s: %w", curDest, err)
			}
			written += uint64(len(fr.Payload))
		case protocol.MsgFileEnd:
			if curFile == nil {
				return 0, fmt.Errorf("protocol: unexpected end marker without FILE_HEADER")
			}
			if written != curSize {
				return 0, fmt.Errorf("protocol: %s truncated: got %d of %d bytes", curName, written, curSize)
			}
			if err := curFile.Close(); err != nil {
				return 0, fmt.Errorf("closing %s: %w", curDest, err)
			}
			curFile = nil
			if watch {
				if seenFiles[curName] {
					infof("received updated %s\n", curName)
				} else {
					seenFiles[curName] = true
					fmt.Fprintln(stdout, curName)
				}
			} else {
				fmt.Fprintln(stdout, curName)
			}
			received++
			curName, curDest, curSize, written = "", "", 0, 0
		case protocol.MsgStreamChunk, protocol.MsgStreamEnd, protocol.MsgRoomInfo:
			return 0, fmt.Errorf("protocol: unexpected message 0x%02x in file mode", byte(fr.Type))
		}
	}
}

func receiveStream(conn net.Conn, stdout io.Writer) error {
	// Clear the handshake deadline: a stream may be idle indefinitely
	// (e.g. tail -f or bursty producer), so no idle timeout applies.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	for {
		fr, err := protocol.ReadFrame(conn, protocol.MaxFrame)
		if err != nil {
			if isEOF(err) && !errors.Is(err, io.ErrUnexpectedEOF) {
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
