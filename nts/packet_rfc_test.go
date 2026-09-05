package nts

import (
	"bytes"
	"encoding/binary"
	"gosuda.org/gstime/ntp"
	"testing"
)

// Independent RFC 8915 section 5.6 oracle: associated data ends BEFORE the
// Authenticator extension; it does not include its type/length/nonce prefix.
func TestPacketAssociatedDataRFC8915(t *testing.T) {
	aead, err := NewAEAD(15, bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	header := (&ntp.Packet{Version: 4, Mode: 3}).Encode()
	ext, _, err := BuildProtectedRequest(header, bytes.Repeat([]byte{1}, 32), 0, aead, nil)
	if err != nil {
		t.Fatal(err)
	}
	fields, _, err := ntp.ParseExtensionFields(ext)
	if err != nil {
		t.Fatal(err)
	}
	auth := fields[len(fields)-1].Wire
	nlen, clen := int(binary.BigEndian.Uint16(auth[4:6])), int(binary.BigEndian.Uint16(auth[6:8]))
	ad := append(append([]byte{}, header...), ext[:len(ext)-len(auth)]...)
	if _, err := aead.Open(nil, auth[8:8+nlen], auth[8+nlen:8+nlen+clen], ad); err != nil {
		t.Errorf("client request fails RFC associated-data verification: %v", err)
	}

	header = (&ntp.Packet{Version: 4, Mode: 4, Stratum: 1}).Encode()
	uid := bytes.Repeat([]byte{7}, 32)
	pre := ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: ExtTypeUniqueID, Value: uid}})
	nonce := bytes.Repeat([]byte{8}, aead.NonceSize())
	ad = append(append([]byte{}, header...), pre...)
	ciphertext := aead.Seal(nil, nonce, nil, ad)
	body := make([]byte, 4+len(nonce)+len(ciphertext))
	binary.BigEndian.PutUint16(body[:2], uint16(len(nonce)))
	binary.BigEndian.PutUint16(body[2:4], uint16(len(ciphertext)))
	copy(body[4:], nonce)
	copy(body[4+len(nonce):], ciphertext)
	ext = append(pre, ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: ExtTypeAuthenticator, Value: body}})...)
	fields, _, err = ntp.ParseExtensionFields(ext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndDecryptResponse(header, fields, uid, aead); err != nil {
		t.Errorf("RFC-authenticated server response rejected: %v", err)
	}
}
