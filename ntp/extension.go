package ntp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ExtensionField represents an NTPv4 extension field (RFC 7822 / RFC 8915).
type ExtensionField struct {
	Type  uint16
	Value []byte // Body excluding 4-byte header and trailing padding
	Wire  []byte // Complete raw wire bytes including header and padding
}

var (
	ErrMalformedExtension = errors.New("malformed extension field")
)

// ParseExtensionFields parses extension fields from the bytes trailing the 48-byte NTP header.
func ParseExtensionFields(data []byte) ([]ExtensionField, []byte, error) {
	var fields []ExtensionField
	offset := 0

	for offset < len(data) {
		remaining := len(data) - offset
		if remaining < 4 {
			// Not enough for an extension field header; treat as trailing/MAC or error
			break
		}

		fieldType := binary.BigEndian.Uint16(data[offset : offset+2])
		fieldLen := binary.BigEndian.Uint16(data[offset+2 : offset+4])

		if fieldLen < 4 {
			return nil, nil, fmt.Errorf("%w: field length %d < 4 at offset %d", ErrMalformedExtension, fieldLen, offset)
		}
		if int(fieldLen) > remaining {
			return nil, nil, fmt.Errorf("%w: field length %d exceeds remaining %d at offset %d", ErrMalformedExtension, fieldLen, remaining, offset)
		}
		if fieldLen%4 != 0 {
			return nil, nil, fmt.Errorf("%w: field length %d not a multiple of 4", ErrMalformedExtension, fieldLen)
		}

		wireBytes := data[offset : offset+int(fieldLen)]
		body := wireBytes[4:]

		fields = append(fields, ExtensionField{
			Type:  fieldType,
			Value: body,
			Wire:  wireBytes,
		})

		offset += int(fieldLen)
	}

	trailing := data[offset:]
	return fields, trailing, nil
}

// EncodeExtensionFields serializes a slice of extension fields.
func EncodeExtensionFields(fields []ExtensionField) []byte {
	var totalLen int
	for _, f := range fields {
		bodyLen := len(f.Value)
		pad := (4 - (bodyLen % 4)) % 4
		totalLen += 4 + bodyLen + pad
	}

	out := make([]byte, totalLen)
	offset := 0

	for _, f := range fields {
		bodyLen := len(f.Value)
		pad := (4 - (bodyLen % 4)) % 4
		wireLen := 4 + bodyLen + pad

		binary.BigEndian.PutUint16(out[offset:offset+2], f.Type)
		binary.BigEndian.PutUint16(out[offset+2:offset+4], uint16(wireLen))
		copy(out[offset+4:offset+4+bodyLen], f.Value)
		// Padding bytes remain 0
		offset += wireLen
	}

	return out
}
