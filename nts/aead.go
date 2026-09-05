package nts

import (
	"crypto/cipher"
	"fmt"

	"github.com/secure-io/siv-go"
)

// NewAEAD creates a cipher.AEAD instance for the given registered AEAD ID.
func NewAEAD(aeadID uint16, key []byte) (cipher.AEAD, error) {
	params, ok := Registry[aeadID]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAead, aeadID)
	}

	if len(key) != params.KeyLength {
		return nil, fmt.Errorf("invalid key length for AEAD %d: got %d want %d",
			aeadID, len(key), params.KeyLength)
	}

	switch aeadID {
	case 15:
		return siv.NewCMAC(key)
	case 30:
		return siv.NewGCM(key)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAead, aeadID)
	}
}
