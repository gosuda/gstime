package nts

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gosuda/gstime/ntp"
)

const (
	ExtTypeUniqueID          uint16 = 0x0104
	ExtTypeCookie            uint16 = 0x0204
	ExtTypeCookiePlaceholder uint16 = 0x0304
	ExtTypeAuthenticator     uint16 = 0x0404
)

var (
	ErrAuthFailed           = errors.New("nts authentication verification failed")
	ErrMissingUniqueID      = errors.New("nts response missing unique identifier extension field")
	ErrUniqueIDMismatch     = errors.New("nts response unique identifier mismatch")
	ErrMissingAuthenticator = errors.New("nts response missing authenticator field")
	ErrAuthNotLast          = errors.New("nts authenticator is not the last extension field")
	ErrCookieStarvation     = errors.New("no unused cookies available for request")
)

// AuthenticatorField decodes the contents of an RFC 8915 Authenticator extension field.
type AuthenticatorField struct {
	Nonce      []byte
	Ciphertext []byte
	WireLength int
}

// BuildProtectedRequest creates the complete extension field payload for an NTS-protected NTP request.
func BuildProtectedRequest(
	ntpHeader []byte,
	cookie []byte,
	placeholderCount int,
	aead cipher.AEAD,
	c2sKey []byte,
) (extBytes []byte, uniqueID []byte, err error) {
	// 1. Generate fresh Unique Identifier (32 bytes)
	uniqueID = make([]byte, 32)
	if _, err := rand.Read(uniqueID); err != nil {
		return nil, nil, fmt.Errorf("failed to generate random unique id: %w", err)
	}

	var fields []ntp.ExtensionField

	// Unique ID field
	fields = append(fields, ntp.ExtensionField{
		Type:  ExtTypeUniqueID,
		Value: uniqueID,
	})

	// Cookie field
	fields = append(fields, ntp.ExtensionField{
		Type:  ExtTypeCookie,
		Value: cookie,
	})

	// Cookie Placeholder fields: must have same length as Cookie field, body all zeros
	for i := 0; i < placeholderCount; i++ {
		zeros := make([]byte, len(cookie))
		fields = append(fields, ntp.ExtensionField{
			Type:  ExtTypeCookiePlaceholder,
			Value: zeros,
		})
	}

	encodedPreAuth := ntp.EncodeExtensionFields(fields)

	// Nonce for AEAD
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("failed to generate random nonce: %w", err)
	}

	// In client request, plaintext is empty
	var plaintext []byte

	// Calculate Authenticator field length:
	// Header (4) + NonceLen (2) + CiphertextLen (2) + len(nonce) + len(ciphertext)
	// Ciphertext length for empty plaintext is tag size (aead.Overhead())
	cipherLen := len(plaintext) + aead.Overhead()
	rawBodyLen := 2 + 2 + len(nonce) + cipherLen
	pad := (4 - (rawBodyLen % 4)) % 4
	totalAuthFieldLen := 4 + rawBodyLen + pad

	// Build Associated Data (AD):
	// NTP header (48 bytes) || Encoded Pre-Auth Fields || Type(0x0404) || FieldLen || NonceLen || CipherLen
	var ad []byte
	ad = append(ad, ntpHeader...)
	ad = append(ad, encodedPreAuth...)

	authPrefix := make([]byte, 8)
	binary.BigEndian.PutUint16(authPrefix[0:2], ExtTypeAuthenticator)
	binary.BigEndian.PutUint16(authPrefix[2:4], uint16(totalAuthFieldLen))
	binary.BigEndian.PutUint16(authPrefix[4:6], uint16(len(nonce)))
	binary.BigEndian.PutUint16(authPrefix[6:8], uint16(cipherLen))
	ad = append(ad, authPrefix...)

	ciphertext := aead.Seal(nil, nonce, plaintext, ad)

	// Encode Authenticator extension field
	authBody := make([]byte, rawBodyLen+pad)
	binary.BigEndian.PutUint16(authBody[0:2], uint16(len(nonce)))
	binary.BigEndian.PutUint16(authBody[2:4], uint16(len(ciphertext)))
	copy(authBody[4:4+len(nonce)], nonce)
	copy(authBody[4+len(nonce):4+len(nonce)+len(ciphertext)], ciphertext)

	authField := ntp.ExtensionField{
		Type:  ExtTypeAuthenticator,
		Value: authBody,
	}

	fields = append(fields, authField)
	totalExt := ntp.EncodeExtensionFields(fields)

	return totalExt, uniqueID, nil
}

// VerifyAndDecryptResponse validates an incoming NTS response according to Section 3.8.
func VerifyAndDecryptResponse(
	ntpHeader []byte,
	extFields []ntp.ExtensionField,
	expectedUniqueID []byte,
	aead cipher.AEAD,
) (replenishedCookies [][]byte, err error) {
	if len(extFields) == 0 {
		return nil, ErrMissingAuthenticator
	}

	// 1. Authenticator MUST be in strict last position
	lastField := extFields[len(extFields)-1]
	if lastField.Type != ExtTypeAuthenticator {
		return nil, ErrAuthNotLast
	}

	// 2. Locate and check Unique ID
	var foundUniqueID []byte
	for i := 0; i < len(extFields)-1; i++ {
		if extFields[i].Type == ExtTypeUniqueID {
			foundUniqueID = extFields[i].Value
			break
		}
	}
	if foundUniqueID == nil {
		return nil, ErrMissingUniqueID
	}

	// Compare Unique ID
	if len(foundUniqueID) < len(expectedUniqueID) {
		return nil, ErrUniqueIDMismatch
	}
	for i := range expectedUniqueID {
		if foundUniqueID[i] != expectedUniqueID[i] {
			return nil, ErrUniqueIDMismatch
		}
	}

	// 3. Parse Authenticator field
	authWire := lastField.Wire
	if len(authWire) < 12 { // 4-byte header + 2 noncelen + 2 cipherlen + ...
		return nil, errors.New("authenticator wire too short")
	}

	nonceLen := int(binary.BigEndian.Uint16(authWire[4:6]))
	cipherLen := int(binary.BigEndian.Uint16(authWire[6:8]))

	if len(authWire) < 8+nonceLen+cipherLen {
		return nil, errors.New("authenticator wire shorter than declared nonce and ciphertext")
	}

	nonce := authWire[8 : 8+nonceLen]
	ciphertext := authWire[8+nonceLen : 8+nonceLen+cipherLen]

	// 4. Construct Associated Data:
	// NTP header (48 bytes) || Encoded Pre-Auth Fields || Authenticator 8-byte prefix
	preAuthFields := extFields[:len(extFields)-1]
	encodedPreAuth := ntp.EncodeExtensionFields(preAuthFields)

	var ad []byte
	ad = append(ad, ntpHeader...)
	ad = append(ad, encodedPreAuth...)
	ad = append(ad, authWire[:8]...) // Type, Length, NonceLen, CipherLen

	plaintext, err := aead.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}

	// 5. Decrypted plaintext contains inner extension fields (e.g. New Cookie fields 0x0204)
	if len(plaintext) > 0 {
		innerFields, _, err := ntp.ParseExtensionFields(plaintext)
		if err == nil {
			for _, inF := range innerFields {
				if inF.Type == ExtTypeCookie {
					replenishedCookies = append(replenishedCookies, inF.Value)
				}
			}
		}
	}

	return replenishedCookies, nil
}
