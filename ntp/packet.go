package ntp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	HeaderSize        = 48
	NtpToUnixOffset   = int64(2208988800) // seconds between 1900-01-01 and 1970-01-01
	MaxCoarseErrorSec = int64(68 * 365 * 86400) // ~68 years (~2^31 seconds)
)

var (
	ErrPacketTooShort = errors.New("ntp packet is smaller than 48 bytes")
	ErrEraAmbiguous   = errors.New("ntp era unfolding ambiguous: zero or multiple candidates")
)

// Packet represents a parsed NTPv4 packet.
type Packet struct {
	LeapIndicator uint8
	Version       uint8
	Mode          uint8
	Stratum       uint8
	Poll          int8
	Precision     int8
	RootDelay     int32  // signed 16.16
	RootDisp      uint32 // unsigned 16.16
	RefID         [4]byte
	RefTimestamp  [8]byte
	OrigTimestamp [8]byte
	RecvTimestamp [8]byte
	TxTimestamp   [8]byte
	Extension     []byte
	Raw           []byte
}

// ParsePacket decodes an NTP packet from wire octets.
func ParsePacket(b []byte) (*Packet, error) {
	if len(b) < HeaderSize {
		return nil, ErrPacketTooShort
	}

	p := &Packet{
		LeapIndicator: (b[0] >> 6) & 0x03,
		Version:       (b[0] >> 3) & 0x07,
		Mode:          b[0] & 0x07,
		Stratum:       b[1],
		Poll:          int8(b[2]),
		Precision:     int8(b[3]),
		RootDelay:     int32(binary.BigEndian.Uint32(b[4:8])),
		RootDisp:      binary.BigEndian.Uint32(b[8:12]),
	}
	copy(p.RefID[:], b[12:16])
	copy(p.RefTimestamp[:], b[16:24])
	copy(p.OrigTimestamp[:], b[24:32])
	copy(p.RecvTimestamp[:], b[32:40])
	copy(p.TxTimestamp[:], b[40:48])

	if len(b) > HeaderSize {
		p.Extension = make([]byte, len(b)-HeaderSize)
		copy(p.Extension, b[HeaderSize:])
	}

	p.Raw = make([]byte, len(b))
	copy(p.Raw, b)
	return p, nil
}

// Encode serializes the NTP packet header and any extension data.
func (p *Packet) Encode() []byte {
	size := HeaderSize + len(p.Extension)
	b := make([]byte, size)

	b[0] = ((p.LeapIndicator & 0x03) << 6) | ((p.Version & 0x07) << 3) | (p.Mode & 0x07)
	b[1] = p.Stratum
	b[2] = byte(p.Poll)
	b[3] = byte(p.Precision)
	binary.BigEndian.PutUint32(b[4:8], uint32(p.RootDelay))
	binary.BigEndian.PutUint32(b[8:12], p.RootDisp)
	copy(b[12:16], p.RefID[:])
	copy(b[16:24], p.RefTimestamp[:])
	copy(b[24:32], p.OrigTimestamp[:])
	copy(b[32:40], p.RecvTimestamp[:])
	copy(b[40:48], p.TxTimestamp[:])

	if len(p.Extension) > 0 {
		copy(b[HeaderSize:], p.Extension)
	}
	return b
}

// RootDelaySeconds converts root delay to float64 seconds.
func (p *Packet) RootDelaySeconds() float64 {
	return float64(p.RootDelay) / 65536.0
}

// RootDispersionSeconds converts root dispersion to float64 seconds.
func (p *Packet) RootDispersionSeconds() float64 {
	return float64(p.RootDisp) / 65536.0
}

// RootDelayNanoseconds converts root delay to nanoseconds.
func (p *Packet) RootDelayNanoseconds() int64 {
	return int64(float64(p.RootDelay) * 1_000_000_000.0 / 65536.0)
}

// RootDispersionNanoseconds converts root dispersion to nanoseconds.
func (p *Packet) RootDispersionNanoseconds() int64 {
	return int64(float64(p.RootDisp) * 1_000_000_000.0 / 65536.0)
}

// DecodeTimestampRaw extracts seconds and fraction from an 8-byte NTP timestamp.
func DecodeTimestampRaw(b [8]byte) (sec uint32, frac uint32) {
	sec = binary.BigEndian.Uint32(b[0:4])
	frac = binary.BigEndian.Uint32(b[4:8])
	return sec, frac
}

// EncodeTimestamp packs seconds and fraction into an 8-byte NTP timestamp.
func EncodeTimestamp(sec uint32, frac uint32) [8]byte {
	var b [8]byte
	binary.BigEndian.PutUint32(b[0:4], sec)
	binary.BigEndian.PutUint32(b[4:8], frac)
	return b
}

// UnfoldNtpSeconds unfolds a 32-bit NTP second into 64-bit NTP seconds using a coarse anchor.
func UnfoldNtpSeconds(s32 uint32, coarseUnixSec int64) (int64, error) {
	coarseNtpSec := coarseUnixSec + NtpToUnixOffset
	if coarseNtpSec < 0 {
		coarseNtpSec = 0
	}

	baseEra := coarseNtpSec >> 32
	var matchingCandidate int64
	matchCount := 0

	for era := baseEra - 1; era <= baseEra + 1; era++ {
		if era < 0 {
			continue
		}
		cand := (era << 32) | int64(s32)
		dist := cand - coarseNtpSec
		if dist < 0 {
			dist = -dist
		}
		if dist <= MaxCoarseErrorSec {
			matchingCandidate = cand
			matchCount++
		}
	}

	if matchCount != 1 {
		return 0, fmt.Errorf("%w: found %d matches for s32=%d, coarseUnixSec=%d",
			ErrEraAmbiguous, matchCount, s32, coarseUnixSec)
	}

	return matchingCandidate, nil
}

// NtpFractionToNanos converts a 32-bit NTP fraction to nanoseconds with round-to-nearest.
func NtpFractionToNanos(frac uint32) int64 {
	return int64((uint64(frac)*1_000_000_000 + (1 << 31)) >> 32)
}

// NtpFractionToNanosFloor converts a 32-bit NTP fraction to nanoseconds rounding toward -infinity.
func NtpFractionToNanosFloor(frac uint32) int64 {
	return int64((uint64(frac) * 1_000_000_000) >> 32)
}

// NtpFractionToNanosCeil converts a 32-bit NTP fraction to nanoseconds rounding toward +infinity.
func NtpFractionToNanosCeil(frac uint32) int64 {
	prod := uint64(frac) * 1_000_000_000
	q := prod >> 32
	if (prod & 0xffffffff) != 0 {
		q++
	}
	return int64(q)
}

// UnfoldTimestamp converts an 8-byte NTP timestamp to Unix nanoseconds using a coarse anchor.
func UnfoldTimestamp(raw [8]byte, coarseUnixSec int64) (int64, error) {
	sec32, frac32 := DecodeTimestampRaw(raw)
	if sec32 == 0 && frac32 == 0 {
		return 0, errors.New("zero timestamp represents unknown/unsynchronized")
	}

	unfoldedNtp, err := UnfoldNtpSeconds(sec32, coarseUnixSec)
	if err != nil {
		return 0, err
	}

	unixSec := unfoldedNtp - NtpToUnixOffset
	nanos := NtpFractionToNanos(frac32)
	totalNanos := unixSec*1_000_000_000 + nanos
	return totalNanos, nil
}
