package room

import (
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
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	size := fi.Size()
	name := filepath.Base(path)
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
			return fmt.Errorf("reading %s: %w", path, rerr)
		}
	}
	if sent != size {
		return fmt.Errorf("%s: size changed during send (%d != %d)", path, sent, size)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(frameTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgFileEnd, nil); err != nil {
		return fmt.Errorf("sending end for %s: %w", name, err)
	}
	return nil
}
