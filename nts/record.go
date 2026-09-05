package nts

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	RecordEndOfMessage        uint16 = 0
	RecordNextProtocol        uint16 = 1
	RecordError               uint16 = 2
	RecordWarning             uint16 = 3
	RecordAeadNegotiation     uint16 = 4
	RecordNewCookie           uint16 = 5
	RecordServerNegotiation   uint16 = 6
	RecordPortNegotiation     uint16 = 7
	RecordCompliantExporter   uint16 = 1024
)

var (
	ErrRecordTooShort     = errors.New("nts-ke record shorter than 4-byte header")
	ErrRecordCapExceeded  = errors.New("nts-ke record exceeded maximum capacity")
	ErrUnknownCritical    = errors.New("unknown critical nts-ke record")
	ErrWarningEncountered = errors.New("nts-ke server returned warning record")
	ErrMissingEndOfMessage= errors.New("nts-ke response missing end-of-message record as last record")
	ErrNtsKeFailed        = errors.New("nts-ke key exchange failed")
)

// Record represents a single framed NTS-KE record.
type Record struct {
	Critical bool
	Type     uint16
	Body     []byte
}

// EncodeRecord serializes a single NTS-KE record. Record bodies are byte-aligned.
func EncodeRecord(r Record) []byte {
	typeWord := r.Type & 0x7fff
	if r.Critical {
		typeWord |= 0x8000
	}

	bodyLen := len(r.Body)
	out := make([]byte, 4+bodyLen)
	binary.BigEndian.PutUint16(out[0:2], typeWord)
	binary.BigEndian.PutUint16(out[2:4], uint16(bodyLen))
	copy(out[4:], r.Body)
	return out
}

// ParseRecords decodes all NTS-KE records from a bounded byte buffer.
func ParseRecords(data []byte, maxRecordCap int) ([]Record, error) {
	var records []Record
	offset := 0

	for offset < len(data) {
		if len(data)-offset < 4 {
			return nil, ErrRecordTooShort
		}

		typeWord := binary.BigEndian.Uint16(data[offset : offset+2])
		bodyLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))

		critical := (typeWord & 0x8000) != 0
		recType := typeWord & 0x7fff

		if maxRecordCap > 0 && bodyLen > maxRecordCap {
			return nil, fmt.Errorf("%w: %d > %d", ErrRecordCapExceeded, bodyLen, maxRecordCap)
		}

		if len(data)-offset < 4+bodyLen {
			return nil, fmt.Errorf("nts-ke record body truncated: need %d bytes, have %d",
				bodyLen, len(data)-offset-4)
		}

		body := make([]byte, bodyLen)
		copy(body, data[offset+4:offset+4+bodyLen])

		records = append(records, Record{
			Critical: critical,
			Type:     recType,
			Body:     body,
		})

		offset += 4 + bodyLen
	}

	return records, nil
}

// BuildClientRequest constructs the initial client NTS-KE request.
func BuildClientRequest(offeredAeads []uint16, includeExporterMarker bool) []byte {
	var out []byte

	// 1. Next Protocol: NTPv4 (protocol ID 0)
	nextProtoBody := make([]byte, 2)
	binary.BigEndian.PutUint16(nextProtoBody, 0)
	out = append(out, EncodeRecord(Record{
		Critical: true,
		Type:     RecordNextProtocol,
		Body:     nextProtoBody,
	})...)

	// 2. AEAD Algorithm Negotiation
	aeadBody := make([]byte, len(offeredAeads)*2)
	for i, id := range offeredAeads {
		binary.BigEndian.PutUint16(aeadBody[i*2:(i+1)*2], id)
	}
	out = append(out, EncodeRecord(Record{
		Critical: true,
		Type:     RecordAeadNegotiation,
		Body:     aeadBody,
	})...)

	// 3. Optional compliant-exporter capability marker
	if includeExporterMarker {
		out = append(out, EncodeRecord(Record{
			Critical: false,
			Type:     RecordCompliantExporter,
			Body:     nil,
		})...)
	}

	// 4. End of Message (must be last, critical=true, body empty)
	out = append(out, EncodeRecord(Record{
		Critical: true,
		Type:     RecordEndOfMessage,
		Body:     nil,
	})...)

	return out
}
