package siv

import (
	"crypto/cipher"
	"crypto/subtle"
)

const poly128 = 0x87

type cmac struct {
	cipher cipher.Block
	k1     [16]byte
	k2     [16]byte
	buf    [16]byte
	off    int
}

func newCMAC(c cipher.Block) *cmac {
	m := &cmac{
		cipher: c,
	}

	var l [16]byte
	c.Encrypt(l[:], l[:])

	msbL := shiftLeft(m.k1[:], l[:])
	m.k1[15] ^= byte(subtle.ConstantTimeSelect(msbL, poly128, 0))

	msbK1 := shiftLeft(m.k2[:], m.k1[:])
	m.k2[15] ^= byte(subtle.ConstantTimeSelect(msbK1, poly128, 0))

	return m
}

func (m *cmac) BlockSize() int { return 16 }
func (m *cmac) Size() int      { return 16 }

func (m *cmac) Reset() {
	for i := range m.buf {
		m.buf[i] = 0
	}
	m.off = 0
}

func (m *cmac) Write(p []byte) (int, error) {
	n := len(p)
	if m.off > 0 {
		needed := 16 - m.off
		if n > needed {
			xorBytes(m.buf[m.off:], p[:needed])
			p = p[needed:]
			m.cipher.Encrypt(m.buf[:], m.buf[:])
			m.off = 0
		} else {
			xorBytes(m.buf[m.off:], p)
			m.off += n
			return n, nil
		}
	}

	for len(p) > 16 {
		xorBytes(m.buf[:], p[:16])
		m.cipher.Encrypt(m.buf[:], m.buf[:])
		p = p[16:]
	}

	if len(p) > 0 {
		xorBytes(m.buf[m.off:], p)
		m.off += len(p)
	}

	return n, nil
}

func (m *cmac) Sum(b []byte) []byte {
	return m.SumWithTagSize(b, 16)
}

func (m *cmac) SumWithTagSize(b []byte, tagSize int) []byte {
	var block [16]byte
	if m.off < 16 {
		copy(block[:], m.k2[:])
		xorBytes(block[:], m.buf[:])
		block[m.off] ^= 0x80
	} else {
		copy(block[:], m.k1[:])
		xorBytes(block[:], m.buf[:])
	}
	m.cipher.Encrypt(block[:], block[:])
	return append(b, block[:tagSize]...)
}

func shiftLeft(dst, src []byte) int {
	var carry byte
	for i := len(src) - 1; i >= 0; i-- {
		bit := src[i] >> 7
		dst[i] = (src[i] << 1) | carry
		carry = bit
	}
	return int(carry)
}

func xorBytes(dst, src []byte) {
	for i := range src {
		dst[i] ^= src[i]
	}
}
