package nts

import (
	"crypto/cipher"
	"errors"
	"fmt"

	"gosuda.org/gstime/ntp"
)

const (
	// MaxPacketSize is the UDP payload ceiling usable with both IPv4 and IPv6
	// without jumbograms: 65535 minus the IPv4 header (20) and UDP header (8).
	// Receivers should read into MaxPacketSize+1 bytes and reject excess data.
	MaxPacketSize = 65535 - 20 - 8

	// PreferredPacketSize leaves room for IPv6 and UDP headers at the minimum
	// IPv6 MTU. It limits optional placeholders, not the mandatory cookie.
	// Large cookies can still require fragmentation and may fail on some paths.
	PreferredPacketSize = 1280 - 40 - 8

	// MaxPlaceholderCount follows RFC 8915 section 5.7's cookie inventory target.
	MaxPlaceholderCount = 7

	// MaxCookieSize leaves room for the NTP header, 32-byte UID, cookie field
	// header, and the largest authenticator used by our supported AEADs (15/30).
	// NTP extension lengths are multiples of four. This common bound ensures
	// every cookie retained by the KE parser or jar fits at least one request.
	MaxCookieSize = (MaxPacketSize &^ 3) - 48 - 36 - 4 - 40
)

var (
	ErrInvalidCookie  = errors.New("nts cookie is empty or exceeds the packet budget")
	ErrPacketTooLarge = errors.New("nts packet exceeds the UDP payload limit")
)

func validCookieSize(size int) bool {
	return size > 0 && size <= MaxCookieSize
}

func paddedLength(size int) int {
	return (size + 3) &^ 3
}

// requestPlaceholderCount budgets before any per-cookie allocations. A large
// mandatory cookie is allowed up to the hard UDP limit; optional placeholders
// must fit the preferred MTU budget (RFC 8915 section 5.7).
func requestPlaceholderCount(cookieSize, requested int, aead cipher.AEAD) (int, error) {
	if !validCookieSize(cookieSize) {
		return 0, ErrInvalidCookie
	}
	if requested < 0 {
		return 0, errors.New("negative NTS cookie placeholder count")
	}
	if aead == nil {
		return 0, errors.New("missing NTS AEAD")
	}
	nonceSize, tagSize := aead.NonceSize(), aead.Overhead()
	if nonceSize <= 0 || nonceSize > 65535 || tagSize <= 0 || tagSize > 65535 {
		return 0, errors.New("invalid NTS AEAD nonce or tag size")
	}
	baseSize := 48 + 36 + 8 + paddedLength(nonceSize) + paddedLength(tagSize)
	cookieFieldSize := 4 + paddedLength(cookieSize)
	if baseSize+cookieFieldSize > MaxPacketSize {
		return 0, ErrPacketTooLarge
	}
	available := max(0, (PreferredPacketSize-baseSize)/cookieFieldSize-1)
	return min(requested, MaxPlaceholderCount, available), nil
}

// Check both wire and value lengths: callers normally provide parsed fields,
// but neither malformed constructed fields nor re-encoding may evade the cap.
func checkResponsePacketSize(header []byte, fields []ntp.ExtensionField) error {
	if len(header) != 48 {
		return errors.New("NTS requires a 48-byte NTP header")
	}
	total := len(header)
	for _, field := range fields {
		if len(field.Value) > MaxPacketSize || len(field.Wire) > MaxPacketSize {
			return ErrPacketTooLarge
		}
		size := max(4+paddedLength(len(field.Value)), len(field.Wire))
		if size > MaxPacketSize-total {
			return fmt.Errorf("%w: extension fields exceed %d bytes", ErrPacketTooLarge, MaxPacketSize)
		}
		total += size
	}
	return nil
}
