package siv

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
)

type aesSivCMAC struct {
	macBlock   cipher.Block
	cryptBlock cipher.Block
}

// NewCMAC returns a cipher.AEAD implementing AES-SIV-CMAC (RFC 5297).
// Key must be 32, 48, or 64 bytes.
func NewCMAC(key []byte) (cipher.AEAD, error) {
	k := len(key)
	if k != 32 && k != 48 && k != 64 {
		return nil, ErrInvalidKeySize
	}
	half := k / 2
	macBlock, err := aes.NewCipher(key[:half])
	if err != nil {
		return nil, err
	}
	cryptBlock, err := aes.NewCipher(key[half:])
	if err != nil {
		return nil, err
	}
	return &aesSivCMAC{
		macBlock:   macBlock,
		cryptBlock: cryptBlock,
	}, nil
}

func (s *aesSivCMAC) NonceSize() int { return 16 }
func (s *aesSivCMAC) Overhead() int  { return 16 }

func (s *aesSivCMAC) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	if n := len(nonce); n != 0 && n != 16 {
		panic("siv: incorrect nonce length given to AES-SIV-CMAC")
	}
	ret, ciphertext := sliceForAppend(dst, 16+len(plaintext))
	mac := newCMAC(s.macBlock)
	v := s2v(additionalData, nonce, plaintext, mac)
	copy(ciphertext[:16], v[:])

	iv := newIV(v)
	ctr := cipher.NewCTR(s.cryptBlock, iv[:])
	ctr.XORKeyStream(ciphertext[16:], plaintext)
	return ret
}

func (s *aesSivCMAC) Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	if n := len(nonce); n != 0 && n != 16 {
		panic("siv: incorrect nonce length given to AES-SIV-CMAC")
	}
	if len(ciphertext) < 16 {
		return dst, ErrAuthFailed
	}
	ret, plaintext := sliceForAppend(dst, len(ciphertext)-16)

	var tag [16]byte
	copy(tag[:], ciphertext[:16])

	iv := newIV(tag)
	ctr := cipher.NewCTR(s.cryptBlock, iv[:])
	ctr.XORKeyStream(plaintext, ciphertext[16:])

	mac := newCMAC(s.macBlock)
	v := s2v(additionalData, nonce, plaintext, mac)
	if subtle.ConstantTimeCompare(v[:], tag[:]) != 1 {
		for i := range plaintext {
			plaintext[i] = 0
		}
		return dst, ErrAuthFailed
	}
	return ret, nil
}

func s2v(additionalData, nonce, plaintext []byte, mac *cmac) [16]byte {
	var b0, b1 [16]byte
	mac.Write(b0[:])
	copy(b1[:], mac.Sum(nil))
	mac.Reset()

	if len(additionalData) > 0 || len(nonce) > 0 {
		mac.Write(additionalData)
		copy(b0[:], mac.Sum(nil))
		mac.Reset()

		dbl(&b1)
		for i := range b1 {
			b1[i] ^= b0[i]
		}
		if len(nonce) > 0 {
			mac.Write(nonce)
			copy(b0[:], mac.Sum(nil))
			mac.Reset()

			dbl(&b1)
			for i := range b1 {
				b1[i] ^= b0[i]
			}
		}
		for i := range b0 {
			b0[i] = 0
		}
	}

	if len(plaintext) >= 16 {
		n := len(plaintext) - 16
		copy(b0[:], plaintext[n:])
		mac.Write(plaintext[:n])
	} else {
		copy(b0[:], plaintext)
		b0[len(plaintext)] = 0x80
		dbl(&b1)
	}

	for i := range b0 {
		b0[i] ^= b1[i]
	}
	mac.Write(b0[:])
	copy(b0[:], mac.Sum(nil))
	mac.Reset()
	return b0
}

func newIV(v [16]byte) [16]byte {
	v[8] &= 0x7f
	v[12] &= 0x7f
	return v
}

func dbl(b *[16]byte) {
	var carry byte
	for i := 15; i >= 0; i-- {
		bit := b[i] >> 7
		b[i] = (b[i] << 1) | carry
		carry = bit
	}
	b[15] ^= byte(subtle.ConstantTimeSelect(int(carry), 0x87, 0))
}
