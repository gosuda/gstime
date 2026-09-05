package nts

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	MaxKEMessageSize = 64 * 1024
	MaxKERecordSize  = 16 * 1024
)

// KeyExchangeResponse contains a fully validated NTPv4 NTS-KE response.
// Empty ServerName means the TLS peer's IP address, not a fresh DNS resolution.
type KeyExchangeResponse struct {
	AEAD       uint16
	Cookies    [][]byte
	ServerName string
	Port       uint16
}

// ReadServerResponse reads framed records through End of Message. TLS read
// boundaries are unrelated to record or message boundaries. Limits apply to the
// complete response and to each allocation, including unknown optional records.
func ReadServerResponse(r io.Reader, offered []uint16) (*KeyExchangeResponse, error) {
	response := &KeyExchangeResponse{Port: 123}
	seen := make(map[uint16]bool)
	total := 0
	for {
		var header [4]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMissingEndOfMessage, err)
		}
		word := binary.BigEndian.Uint16(header[:2])
		kind := word & 0x7fff
		critical := word&0x8000 != 0
		size := int(binary.BigEndian.Uint16(header[2:]))
		total += 4 + size
		if size > MaxKERecordSize || total > MaxKEMessageSize {
			return nil, ErrRecordCapExceeded
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("truncated NTS-KE record: %w", err)
		}
		switch kind {
		case RecordEndOfMessage:
			if !critical || size != 0 {
				return nil, errors.New("invalid NTS-KE End of Message record")
			}
			if !seen[RecordNextProtocol] || !seen[RecordAeadNegotiation] || len(response.Cookies) == 0 {
				return nil, errors.New("NTS-KE response missing protocol, AEAD, or cookies")
			}
			return response, nil
		case RecordNextProtocol:
			if seen[kind] || !critical || size != 2 || binary.BigEndian.Uint16(body) != 0 {
				return nil, errors.New("NTS-KE must select NTPv4 exactly once")
			}
			seen[kind] = true
		case RecordAeadNegotiation:
			if seen[kind] || size != 2 {
				return nil, errors.New("NTS-KE must select one AEAD exactly once")
			}
			seen[kind] = true
			id := binary.BigEndian.Uint16(body)
			supported := false
			for _, candidate := range offered {
				if candidate == id {
					supported = true
				}
			}
			if _, ok := Registry[id]; !supported || !ok {
				return nil, ErrUnsupportedAead
			}
			response.AEAD = id
		case RecordNewCookie:
			if size == 0 {
				return nil, errors.New("empty NTS cookie")
			}
			response.Cookies = append(response.Cookies, body)
		case RecordError:
			return nil, ErrNtsKeFailed
		case RecordWarning:
			if !critical || size != 2 {
				return nil, errors.New("invalid NTS-KE Warning record")
			}
			// RFC 8915 section 4.1.4 defines no warning codes and requires
			// unrecognized codes to be treated as errors.
			return nil, ErrWarningEncountered
		case RecordServerNegotiation:
			if seen[kind] {
				return nil, errors.New("duplicate NTP server negotiation")
			}
			seen[kind] = true
			name, err := serverName(body)
			if err != nil {
				return nil, err
			}
			response.ServerName = name
		case RecordPortNegotiation:
			if seen[kind] || size != 2 || binary.BigEndian.Uint16(body) == 0 {
				return nil, errors.New("invalid NTP port negotiation")
			}
			seen[kind] = true
			response.Port = binary.BigEndian.Uint16(body)
		case RecordCompliantExporter:
			if seen[kind] || size != 0 {
				return nil, errors.New("invalid compliant exporter marker")
			}
			seen[kind] = true
		default:
			if critical {
				return nil, fmt.Errorf("%w: %d", ErrUnknownCritical, kind)
			}
		}
	}
}

func serverName(body []byte) (string, error) {
	name := string(body)
	if ip := net.ParseIP(name); ip != nil {
		return ip.String(), nil
	}
	name = strings.TrimSuffix(name, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", errors.New("invalid NTP server name")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid NTP server label")
		}
		for _, c := range []byte(label) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("NTP server name must be an ASCII FQDN or IP address")
			}
		}
	}
	return name + ".", nil // RFC 8915 section 4.1.7: never apply a DNS search suffix.
}
