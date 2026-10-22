package vmlink

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frames between QEMU and the tap device (ADR-026). QEMU's -netdev stream sends each Ethernet frame as a
// 4-byte big-endian length and the frame.

// MaxFrame bounds one frame; anything larger ends the connection.
const MaxFrame = 65536

// ErrOversized means the peer sent a frame larger than MaxFrame.
var ErrOversized = errors.New("oversized frame")

// ReadFrame reads one frame into buf and returns it.
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame || int(n) > len(buf) {
		return nil, fmt.Errorf("%w: %d bytes", ErrOversized, n)
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// AppendFrame appends a length-prefixed frame to dst.
func AppendFrame(dst, frame []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(frame)))
	return append(dst, frame...)
}

// FixChecksum recomputes the TCP or UDP checksum of an unfragmented IPv4 frame in place. Docker Desktop
// hands containers packets whose checksums were never filled in (marked already verified); a VM
// verifies them and drops them (ADR-026).
func FixChecksum(f []byte) {
	if len(f) < 14+20 || f[12] != 0x08 || f[13] != 0x00 {
		return
	}
	ip := f[14:]
	ihl := int(ip[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(ip[2:4]))
	if ip[0]>>4 != 4 || ihl < 20 || total > len(ip) || total < ihl {
		return
	}
	if binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 { // more fragments, or a fragment offset
		return
	}
	proto := ip[9]
	l4 := ip[ihl:total]
	off := 0
	switch proto {
	case 6:
		off = 16
	case 17:
		off = 6
		if len(l4) >= 8 && binary.BigEndian.Uint16(l4[6:8]) == 0 {
			return // UDP without a checksum is valid as sent
		}
	default:
		return
	}
	if len(l4) < off+2 {
		return
	}
	l4[off], l4[off+1] = 0, 0
	sum := uint32(proto) + uint32(len(l4))
	for i := 12; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(ip[i : i+2]))
	}
	for i := 0; i+1 < len(l4); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(l4[i : i+2]))
	}
	if len(l4)%2 == 1 {
		sum += uint32(l4[len(l4)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	c := ^uint16(sum)
	if c == 0 && proto == 17 {
		c = 0xffff
	}
	binary.BigEndian.PutUint16(l4[off:off+2], c)
}
