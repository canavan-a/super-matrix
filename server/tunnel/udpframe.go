package tunnel

import (
	"encoding/binary"
	"errors"
	"io"
)

// A UDP bridge carries packets on one stream, each as a frame:
//
//	[4 bytes flow id][2 bytes length][payload]
//
// big-endian. The flow id names the sender on the listening side, so the
// far side can give each sender its own socket and send replies back
// labelled with the same id.

const frameHeader = 6

// maxDatagram is the largest UDP payload a frame can carry.
const maxDatagram = 65535

var errFrameTooBig = errors.New("tunnel: packet too large")

func writeFrame(w io.Writer, flow uint32, p []byte) error {
	if len(p) > maxDatagram {
		return errFrameTooBig
	}
	buf := make([]byte, frameHeader+len(p))
	binary.BigEndian.PutUint32(buf, flow)
	binary.BigEndian.PutUint16(buf[4:], uint16(len(p)))
	copy(buf[frameHeader:], p)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one frame into buf (at least maxDatagram long) and
// returns its flow id and payload, which aliases buf.
func readFrame(r io.Reader, buf []byte) (uint32, []byte, error) {
	var h [frameHeader]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(h[4:]))
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, nil, err
	}
	return binary.BigEndian.Uint32(h[:]), buf[:n], nil
}
