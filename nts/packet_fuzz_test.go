package nts

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"gosuda.org/gstime/ntp"
)

func registeredResponseFixture(t testing.TB, id uint16) (cipher.AEAD, []byte, []byte, []byte) {
	t.Helper()
	aead, err := NewAEAD(id, bytes.Repeat([]byte{0x42}, Registry[id].KeyLength))
	if err != nil {
		t.Fatal(err)
	}
	header := (&ntp.Packet{Version: 4, Mode: 4, Stratum: 1}).Encode()
	uid := bytes.Repeat([]byte{7}, 32)
	cookie := bytes.Repeat([]byte{9}, 32)
	pre := ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: ExtTypeUniqueID, Value: uid}})
	ad := append(append([]byte(nil), header...), pre...)
	nonce := bytes.Repeat([]byte{8}, aead.NonceSize())
	plain := ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: ExtTypeCookie, Value: cookie}})
	sealed := aead.Seal(nil, nonce, plain, ad)
	body := make([]byte, 4+len(nonce)+len(sealed))
	binary.BigEndian.PutUint16(body[:2], uint16(len(nonce)))
	binary.BigEndian.PutUint16(body[2:4], uint16(len(sealed)))
	copy(body[4:], nonce)
	copy(body[4+len(nonce):], sealed)
	wire := append(ad, ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: ExtTypeAuthenticator, Value: body}})...)
	return aead, wire, uid, cookie
}

func verifyResponseWire(t testing.TB, wire, uid []byte, aead cipher.AEAD) ([][]byte, error) {
	t.Helper()
	packet, err := ntp.ParsePacket(wire)
	if err != nil {
		return nil, err
	}
	fields, trailing, err := ntp.ParseExtensionFields(packet.Extension)
	if err != nil {
		return nil, err
	}
	if len(trailing) != 0 {
		return nil, errEncryptedTrailing
	}
	return VerifyAndDecryptResponse(wire[:ntp.HeaderSize], fields, uid, aead)
}

func TestResponseRejectsMalformedRegisteredAEADSizes(t *testing.T) {
	for _, id := range []uint16{15, 30} {
		aead, valid, uid, cookie := registeredResponseFixture(t, id)
		t.Run(fmt.Sprintf("valid_aead_%d", id), func(t *testing.T) {
			cookies, err := verifyResponseWire(t, valid, uid, aead)
			if err != nil || len(cookies) != 1 || !bytes.Equal(cookies[0], cookie) {
				t.Fatalf("registered response must authenticate and replenish its cookie: cookies=%x error=%v", cookies, err)
			}
		})
		const authOffset = ntp.HeaderSize + 4 + 32
		for _, size := range []uint16{0, 1, uint16(aead.NonceSize() - 1), uint16(aead.NonceSize() + 1), 65535} {
			t.Run(fmt.Sprintf("aead_%d_nonce_%d", id, size), func(t *testing.T) {
				wire := bytes.Clone(valid)
				binary.BigEndian.PutUint16(wire[authOffset+4:authOffset+6], size)
				cookies, err := verifyResponseWire(t, wire, uid, aead)
				if !errors.Is(err, errAuthNonceSize) || cookies != nil {
					t.Fatalf("malformed nonce must fail before decryption: cookies=%x error=%v", cookies, err)
				}
			})
		}
		for _, size := range []uint16{0, uint16(aead.Overhead() - 1)} {
			t.Run(fmt.Sprintf("aead_%d_ciphertext_%d", id, size), func(t *testing.T) {
				wire := bytes.Clone(valid)
				binary.BigEndian.PutUint16(wire[authOffset+6:authOffset+8], size)
				cookies, err := verifyResponseWire(t, wire, uid, aead)
				if !errors.Is(err, errAuthCiphertextSize) || cookies != nil {
					t.Fatalf("short ciphertext must fail before decryption: cookies=%x error=%v", cookies, err)
				}
			})
		}
	}
}

func FuzzVerifyAndDecryptResponse(f *testing.F) {
	for _, id := range []uint16{15, 30} {
		aead, valid, _, _ := registeredResponseFixture(f, id)
		f.Add(id, valid)
		f.Add(id, []byte{})
		const authOffset = ntp.HeaderSize + 4 + 32
		for _, nonceSize := range []uint16{0, 1, uint16(aead.NonceSize() - 1), uint16(aead.NonceSize() + 1), 65535} {
			wire := bytes.Clone(valid)
			binary.BigEndian.PutUint16(wire[authOffset+4:authOffset+6], nonceSize)
			f.Add(id, wire)
		}
		for _, cipherSize := range []uint16{0, 15, 65535} {
			wire := bytes.Clone(valid)
			binary.BigEndian.PutUint16(wire[authOffset+6:authOffset+8], cipherSize)
			f.Add(id, wire)
		}
	}
	f.Fuzz(func(t *testing.T, id uint16, wire []byte) {
		params, ok := Registry[id]
		if !ok || len(wire) > MaxPacketSize {
			return
		}
		aead, err := NewAEAD(id, bytes.Repeat([]byte{0x42}, params.KeyLength))
		if err != nil {
			t.Fatal(err)
		}
		cookies, err := verifyResponseWire(t, wire, bytes.Repeat([]byte{7}, 32), aead)
		if err != nil {
			if cookies != nil {
				t.Fatal("rejected response released cookies")
			}
			return
		}
		for _, cookie := range cookies {
			if !validCookieSize(len(cookie)) {
				t.Fatalf("authenticated response released invalid cookie length %d", len(cookie))
			}
		}
	})
}
