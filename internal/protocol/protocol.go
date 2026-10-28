package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// magic prefixes every frame so a receiver can tell Catpult traffic from
// whatever else may answer on the transport port. It names the product, not
// the wire revision — Version does that — so the two failure modes stay
// distinguishable. It is protocol identification, not authentication: any
// peer inside the Tailcat room can send it.
const magic = "EGAD"

// frameHeaderLen is the fixed frame header size: magic | Version | type.
const frameHeaderLen = len(magic) + 2

// Version is the Catpult application protocol version. It follows the magic
// in every frame header; receivers reject anything else.
const Version byte = 1

// Message types. RoomInfo must be the first frame on every connection.
type MsgType byte

const (
	MsgRoomInfo    MsgType = 0x01
	MsgFileHeader  MsgType = 0x02
	MsgFileChunk   MsgType = 0x03
	MsgFileEnd     MsgType = 0x04
	MsgStreamChunk MsgType = 0x05
	MsgStreamEnd   MsgType = 0x06
)

// Payload types announced in ROOM_INFO.
type PayloadType byte

const (
	PayloadFile      PayloadType = 0x00
	PayloadStream    PayloadType = 0x01
	PayloadFileWatch PayloadType = 0x02 // file mode + keep-open/resend semantics
)

func (p PayloadType) String() string {
	switch p {
	case PayloadFile:
		return "file"
	case PayloadStream:
		return "stream"
	case PayloadFileWatch:
		return "file-watch"
	default:
		return "unknown"
	}
}

// Resource limits (spec §11, PLAN steps 3+7).
const (
	// MaxFrame is the largest single frame payload in bytes.
	MaxFrame = 1 << 20 // 1 MiB
	// MaxHeader caps decoded header-ish payloads (ROOM_INFO, FILE header).
	MaxHeader = 4 << 10 // 4 KiB
	// MaxFilenameLen caps filename bytes (also enforced: >255 rejected).
	MaxFilenameLen = 255
	// StreamBufferCap caps the in-memory stream cache for late joiners.
	StreamBufferCap = 64 << 20 // 64 MiB
	// ChunkSize is the sender's read/chunk size for file + stream payloads.
	ChunkSize = 32 << 10 // 32 KiB
)

// Frame is a single versioned, length-delimited message.
type Frame struct {
	Type    MsgType
	Payload []byte
}

// WriteFrame encodes one frame to w as magic | version | type | length | payload.
func WriteFrame(w io.Writer, typ MsgType, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("protocol: payload %d bytes exceeds max frame %d", len(payload), MaxFrame)
	}
	var hdr [frameHeaderLen]byte
	copy(hdr[:], magic)
	hdr[len(magic)] = Version
	hdr[len(magic)+1] = byte(typ)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(payload)))
	if _, err := w.Write(lb[:n]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame decodes one frame from r. It rejects a non-Catpult peer, bad
// versions, unknown types, oversized lengths, and truncated payloads.
// maxPayload overrides MaxFrame for tighter contexts (pass MaxFrame for the
// default); callers may pass a smaller cap such as MaxHeader for header
// frames.
func ReadFrame(r io.Reader, maxPayload int) (Frame, error) {
	if maxPayload <= 0 || maxPayload > MaxFrame {
		maxPayload = MaxFrame
	}
	var hdr [frameHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, fmt.Errorf("protocol: reading frame header: %w", err)
	}
	if string(hdr[:len(magic)]) != magic {
		return Frame{}, fmt.Errorf("protocol: not a catpult peer (bad magic, want %q)", magic)
	}
	if hdr[len(magic)] != Version {
		return Frame{}, fmt.Errorf("protocol: unsupported version %d (want %d)", hdr[len(magic)], Version)
	}
	typ := MsgType(hdr[len(magic)+1])
	switch typ {
	case MsgRoomInfo, MsgFileHeader, MsgFileChunk, MsgFileEnd, MsgStreamChunk, MsgStreamEnd:
	default:
		return Frame{}, fmt.Errorf("protocol: unknown message type 0x%02x", byte(typ))
	}
	br, ok := r.(io.ByteReader)
	if !ok {
		br = bufByteReader{r}
	}
	length, err := binary.ReadUvarint(br)
	if err != nil {
		return Frame{}, fmt.Errorf("protocol: reading frame length: %w", err)
	}
	if length > uint64(maxPayload) {
		return Frame{}, fmt.Errorf("protocol: frame length %d exceeds limit %d", length, maxPayload)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("protocol: reading frame payload: %w", err)
		}
	}
	return Frame{Type: typ, Payload: payload}, nil
}

type bufByteReader struct{ r io.Reader }

func (b bufByteReader) ReadByte() (byte, error) {
	var b1 [1]byte
	_, err := io.ReadFull(b.r, b1[:])
	return b1[0], err
}

// --- ROOM_INFO ---

// EncodeRoomInfo builds a ROOM_INFO payload for the given payload type.
func EncodeRoomInfo(pt PayloadType) []byte {
	return []byte{byte(pt)}
}

// DecodeRoomInfo parses a ROOM_INFO payload.
func DecodeRoomInfo(p []byte) (PayloadType, error) {
	if len(p) != 1 {
		return 0, fmt.Errorf("protocol: ROOM_INFO must be 1 byte, got %d", len(p))
	}
	switch PayloadType(p[0]) {
	case PayloadFile, PayloadStream, PayloadFileWatch:
		return PayloadType(p[0]), nil
	default:
		return 0, fmt.Errorf("protocol: unknown payload_type 0x%02x", p[0])
	}
}

// --- FILE header ---
// Encoding: uvarint(nameLen) + name bytes + uvarint(fileSize).

// EncodeFileHeader builds a FILE header payload. size is the exact byte
// length and is unbounded at the protocol layer (up to 2^64-1); framing
// and per-frame caps still apply, and the transfer streams in ChunkSize
// pieces so large files stay constant-memory.
func EncodeFileHeader(filename string, size uint64) ([]byte, error) {
	name := []byte(filename)
	if len(name) == 0 {
		return nil, fmt.Errorf("protocol: empty filename")
	}
	if len(name) > MaxFilenameLen {
		return nil, fmt.Errorf("protocol: filename %d bytes exceeds limit %d", len(name), MaxFilenameLen)
	}
	var buf bytes.Buffer
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(name)))
	buf.Write(tmp[:n])
	buf.Write(name)
	n = binary.PutUvarint(tmp[:], size)
	buf.Write(tmp[:n])
	if buf.Len() > MaxHeader {
		return nil, fmt.Errorf("protocol: file header too large")
	}
	return buf.Bytes(), nil
}

// DecodeFileHeader parses a FILE header payload.
func DecodeFileHeader(p []byte) (filename string, size uint64, err error) {
	if len(p) > MaxHeader {
		return "", 0, fmt.Errorf("protocol: file header %d bytes exceeds limit %d", len(p), MaxHeader)
	}
	r := bytes.NewReader(p)
	nameLen, err := binary.ReadUvarint(r)
	if err != nil {
		return "", 0, fmt.Errorf("protocol: reading filename length: %w", err)
	}
	if nameLen == 0 {
		return "", 0, fmt.Errorf("protocol: empty filename")
	}
	if nameLen > MaxFilenameLen {
		return "", 0, fmt.Errorf("protocol: filename length %d exceeds limit %d", nameLen, MaxFilenameLen)
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(r, name); err != nil {
		return "", 0, fmt.Errorf("protocol: reading filename: %w", err)
	}
	size, err = binary.ReadUvarint(r)
	if err != nil {
		return "", 0, fmt.Errorf("protocol: reading file size: %w", err)
	}
	if r.Len() != 0 {
		return "", 0, fmt.Errorf("protocol: trailing garbage in file header (%d bytes)", r.Len())
	}
	return string(name), size, nil
}
