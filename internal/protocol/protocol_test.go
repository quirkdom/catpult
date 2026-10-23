package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		typ     MsgType
		payload []byte
	}{
		{MsgRoomInfo, EncodeRoomInfo(PayloadFile)},
		{MsgRoomInfo, EncodeRoomInfo(PayloadStream)},
		{MsgFileChunk, []byte("hello")},
		{MsgStreamChunk, bytes.Repeat([]byte("x"), 100)},
		{MsgFileEnd, nil},
		{MsgStreamEnd, nil},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, c.typ, c.payload); err != nil {
			t.Fatalf("write %v: %v", c.typ, err)
		}
		got, err := ReadFrame(&buf, MaxFrame)
		if err != nil {
			t.Fatalf("read %v: %v", c.typ, err)
		}
		if got.Type != c.typ || !bytes.Equal(got.Payload, c.payload) {
			t.Fatalf("round trip mismatch for %v", c.typ)
		}
	}
}

func TestRoomInfoValidation(t *testing.T) {
	for _, pt := range []PayloadType{PayloadFile, PayloadStream} {
		got, err := DecodeRoomInfo(EncodeRoomInfo(pt))
		if err != nil || got != pt {
			t.Fatalf("roominfo %v: got %v, %v", pt, got, err)
		}
	}
	if _, err := DecodeRoomInfo([]byte{0x42}); err == nil {
		t.Fatal("expected error for unknown payload_type")
	}
	if _, err := DecodeRoomInfo([]byte{}); err == nil {
		t.Fatal("expected error for empty ROOM_INFO")
	}
	if _, err := DecodeRoomInfo([]byte{0, 1}); err == nil {
		t.Fatal("expected error for 2-byte ROOM_INFO")
	}
}

func TestFileHeaderRoundTrip(t *testing.T) {
	p, err := EncodeFileHeader("foo.txt", 1234)
	if err != nil {
		t.Fatal(err)
	}
	name, size, err := DecodeFileHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if name != "foo.txt" || size != 1234 {
		t.Fatalf("got %q %d", name, size)
	}
}

func TestFileHeaderTrailingGarbage(t *testing.T) {
	p, err := EncodeFileHeader("a.txt", 1)
	if err != nil {
		t.Fatal(err)
	}
	p = append(p, 0x00)
	if _, _, err := DecodeFileHeader(p); err == nil {
		t.Fatal("expected trailing-garbage error")
	}
}

func TestBadMagic(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgFileEnd, nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if string(b[:4]) != "EGAD" {
		t.Fatalf("frame does not start with magic: %q", b[:4])
	}
	b[2] = 'X'
	_, err := ReadFrame(bytes.NewReader(b), MaxFrame)
	if err == nil {
		t.Fatal("expected bad-magic error")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Fatalf("bad magic reported as a different fault: %v", err)
	}
}

func TestBadVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgFileEnd, nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[4] = 0x7F
	_, err := ReadFrame(bytes.NewReader(b), MaxFrame)
	if err == nil {
		t.Fatal("expected version error")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("bad version reported as a different fault: %v", err)
	}
}

func TestUnknownType(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgFileEnd, nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	b[5] = 0xFF
	if _, err := ReadFrame(bytes.NewReader(b), MaxFrame); err == nil {
		t.Fatal("expected unknown-type error")
	}
}

func TestTruncatedFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, MsgFileChunk, []byte("hello world")); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	for _, n := range []int{1, 4, 5, 6, len(b) - 1} {
		if _, err := ReadFrame(bytes.NewReader(b[:n]), MaxFrame); err == nil {
			t.Fatalf("expected truncation error at %d bytes", n)
		}
	}
}

func TestOversizedLength(t *testing.T) {
	// Hand-crafted frame header claiming a huge payload with no body.
	raw := []byte{'E', 'G', 'A', 'D', Version, byte(MsgStreamChunk), 0xFF, 0xFF, 0xFF, 0xFF, 0x0F}
	if _, err := ReadFrame(bytes.NewReader(raw), MaxFrame); err == nil {
		t.Fatal("expected oversize error")
	} else if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteOversizedPayload(t *testing.T) {
	big := make([]byte, MaxFrame+1)
	if err := WriteFrame(&bytes.Buffer{}, MsgFileChunk, big); err == nil {
		t.Fatal("expected write oversize error")
	}
}

func TestFileHeaderLimits(t *testing.T) {
	if _, err := EncodeFileHeader("", 1); err == nil {
		t.Fatal("expected empty-name error")
	}
	if _, err := EncodeFileHeader(strings.Repeat("a", MaxFilenameLen+1), 1); err == nil {
		t.Fatal("expected long-name error")
	}
}

func TestFileHeaderLargeSize(t *testing.T) {
	// No per-file total cap: multi-GB sizes must round-trip.
	const big = 5 << 30 // 5 GiB, header-only, no allocation
	p, err := EncodeFileHeader("big.bin", big)
	if err != nil {
		t.Fatal(err)
	}
	name, size, err := DecodeFileHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if name != "big.bin" || size != big {
		t.Fatalf("got %q %d", name, size)
	}
}
