package protocol

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	p := bytes.Repeat([]byte{7}, 3200)
	b, err := EncodeFrame(FrameHeader{Kind: KindAudioChunk, Seq: 3, TurnNo: 1, ChunkIndex: 0, Codec: CodecPCM16}, p)
	if err != nil {
		t.Fatal(err)
	}
	// ejemplo literal del contrato §5.12
	want := []byte{0x43, 0x58, 0x01, 0x01, 0, 0, 0, 3, 0, 0, 0, 1, 0, 0, 0, 0, 0x01, 0x00, 0x0C, 0x80}
	if !bytes.Equal(b[:20], want) {
		t.Fatalf("header % x", b[:20])
	}
	h, got, err := DecodeFrame(b)
	if err != nil || h.Seq != 3 || h.TurnNo != 1 || !bytes.Equal(got, p) || h.Last {
		t.Fatalf("%+v %v", h, err)
	}
	b[19]++ // payload_len inconsistente
	if _, _, err := DecodeFrame(b); err != ErrBadFrame {
		t.Fatal("payload_len inconsistente debe fallar")
	}
	if _, err := EncodeFrame(FrameHeader{}, make([]byte, 32001)); err == nil {
		t.Fatal("payload máximo")
	}
	bad := append([]byte{0x43, 0x58, 1, 1}, make([]byte, 16)...)
	bad[17] = 0x02
	if _, _, err := DecodeFrame(bad); err == nil {
		t.Fatal("flags reservados")
	}
}
