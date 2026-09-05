package nts

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	ExporterLabel = "EXPORTER-network-time-security"
)

var (
	ErrUnsupportedAead = errors.New("unsupported aead algorithm id")
)

// AeadParams defines the compile-time registered parameters for an AEAD algorithm.
type AeadParams struct {
	ID        uint16
	KeyLength int
	NonceSize int
	TagSize   int
}

// Registry of supported AEAD algorithms (Section 3.6).
var Registry = map[uint16]AeadParams{
	15: {
		ID:        15,
		KeyLength: 32, // AES-SIV-CMAC-256 (RFC 5297)
		NonceSize: 16,
		TagSize:   16,
	},
	30: {
		ID:        30,
		KeyLength: 16, // AES-128-GCM-SIV (RFC 8452)
		NonceSize: 12,
		TagSize:   16,
	},
}

// BuildExporterContext constructs the exact 5-byte context: uint16_be(nextProto) || uint16_be(aeadID) || uint8(direction).
func BuildExporterContext(nextProto uint16, aeadID uint16, direction uint8) []byte {
	ctx := make([]byte, 5)
	binary.BigEndian.PutUint16(ctx[0:2], nextProto)
	binary.BigEndian.PutUint16(ctx[2:4], aeadID)
	ctx[4] = direction
	return ctx
}

// KeyExporter is a function that exports keying material from a TLS session (RFC 5705 / RFC 8446).
type KeyExporter func(label string, context []byte, length int) ([]byte, error)

// ExportDirectionalKeys derives C2S and S2C keys using the TLS exporter.
func ExportDirectionalKeys(exporter KeyExporter, nextProto uint16, aeadID uint16) (c2sKey, s2cKey []byte, err error) {
	params, ok := Registry[aeadID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %d", ErrUnsupportedAead, aeadID)
	}

	ctxC2S := BuildExporterContext(nextProto, aeadID, 0)
	c2s, err := exporter(ExporterLabel, ctxC2S, params.KeyLength)
	if err != nil {
		return nil, nil, fmt.Errorf("tls export c2s key failed: %w", err)
	}

	ctxS2C := BuildExporterContext(nextProto, aeadID, 1)
	s2c, err := exporter(ExporterLabel, ctxS2C, params.KeyLength)
	if err != nil {
		return nil, nil, fmt.Errorf("tls export s2c key failed: %w", err)
	}

	return c2s, s2c, nil
}
