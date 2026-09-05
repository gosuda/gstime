package siv

import (
	"errors"
)

var (
	ErrAuthFailed         = errors.New("siv: message authentication failed")
	ErrInvalidKeySize     = errors.New("siv: invalid key size")
	ErrInvalidNonceSize   = errors.New("siv: invalid nonce size")
	ErrCiphertextTooShort = errors.New("siv: ciphertext too short")
)

func sliceForAppend(in []byte, n int) (head, tail []byte) {
	total := len(in) + n
	if cap(in) >= total {
		head = in[:total]
	} else {
		head = make([]byte, total)
		copy(head, in)
	}
	tail = head[len(in):]
	return head, tail
}
