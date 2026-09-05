package siv

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"testing"
)

func fromHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q) failed: %v", s, err)
	}
	return b
}

func TestCMACNISTVectors(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		msg     string
		tag     string
		tagSize int
	}{
		// NIST SP 800-38B AES-128
		{
			name:    "AES-128-empty",
			key:     "2b7e151628aed2a6abf7158809cf4f3c",
			msg:     "",
			tag:     "bb1d6929e95937287fa37d129b756746",
			tagSize: 16,
		},
		{
			name:    "AES-128-16bytes",
			key:     "2b7e151628aed2a6abf7158809cf4f3c",
			msg:     "6bc1bee22e409f96e93d7e117393172a",
			tag:     "070a16b46b4d4144f79bdd9dd04a287c",
			tagSize: 16,
		},
		{
			name:    "AES-128-40bytes",
			key:     "2b7e151628aed2a6abf7158809cf4f3c",
			msg:     "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411",
			tag:     "dfa66747de9ae63030ca32611497c827",
			tagSize: 16,
		},
		{
			name:    "AES-128-40bytes-tag12",
			key:     "2b7e151628aed2a6abf7158809cf4f3c",
			msg:     "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411",
			tag:     "dfa66747de9ae63030ca3261",
			tagSize: 12,
		},
		// NIST SP 800-38B AES-256
		{
			name:    "AES-256-empty",
			key:     "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4",
			msg:     "",
			tag:     "028962f61b7bf89efc6b551f4667d983",
			tagSize: 16,
		},
		{
			name:    "AES-256-16bytes",
			key:     "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4",
			msg:     "6bc1bee22e409f96e93d7e117393172a",
			tag:     "28a7023f452e8f82bd4bf28d8c37c35c",
			tagSize: 16,
		},
		{
			name:    "AES-256-40bytes",
			key:     "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4",
			msg:     "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411",
			tag:     "aaf3d8f1de5640c232f5b169b9c911e6",
			tagSize: 16,
		},
		{
			name:    "AES-256-40bytes-tag12",
			key:     "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4",
			msg:     "6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411",
			tag:     "aaf3d8f1de5640c232f5b169",
			tagSize: 12,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := fromHex(t, tc.key)
			msg := fromHex(t, tc.msg)
			expected := fromHex(t, tc.tag)

			c, err := aes.NewCipher(k)
			if err != nil {
				t.Fatalf("aes.NewCipher failed: %v", err)
			}
			mac := newCMAC(c)
			mac.Write(msg)
			out := mac.SumWithTagSize(nil, tc.tagSize)
			if !bytes.Equal(out, expected) {
				t.Fatalf("CMAC mismatch: got %x want %x", out, expected)
			}
		})
	}
}

func TestAESSIVCMACVectors(t *testing.T) {
	// RFC 5297 Appendix A test vectors
	tests := []struct {
		name       string
		key        string
		plaintext  string
		ad         string
		nonce      string
		ciphertext string
	}{
		{
			name:       "RFC5297-A.1-AES-SIV-CMAC-256",
			key:        "fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff",
			plaintext:  "112233445566778899aabbccddee",
			ad:         "101112131415161718191a1b1c1d1e1f2021222324252627",
			nonce:      "",
			ciphertext: "85632d07c6e8f37f950acd320a2ecc9340c02b9690c4dc04daef7f6afe5c",
		},
		{
			name:       "RFC5297-A.2-AES-SIV-CMAC-512",
			key:        "fffefdfcfbfaf9f8f7f6f5f4f3f2f1f06f6e6d6c6b6a69686766656463626160f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff000102030405060708090a0b0c0d0e0f",
			plaintext:  "112233445566778899aabbccddee",
			ad:         "101112131415161718191a1b1c1d1e1f2021222324252627",
			nonce:      "",
			ciphertext: "f125274c598065cfc26b0e71575029088b035217e380cac8919ee800c126",
		},
		{
			name:       "RFC5297-A.3-EmptyPlaintext",
			key:        "fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff",
			plaintext:  "",
			ad:         "",
			nonce:      "",
			ciphertext: "f2007a5beb2b8900c588a7adf599f172",
		},
		{
			name:       "RFC5297-A.4-16BytePlaintext",
			key:        "fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff",
			plaintext:  "00112233445566778899aabbccddeeff",
			ad:         "",
			nonce:      "",
			ciphertext: "f304f912863e303d5b540e5057c7010c942ffaf45b0e5ca5fb9a56a5263bb065",
		},
		{
			name:       "RFC5297-A.5-WithNonceAndAD",
			key:        "7f7e7d7c7b7a79787776757473727170404142434445464748494a4b4c4d4e4f",
			plaintext:  "7468697320697320736f6d6520706c61696e7465787420746f20656e6372797074207573696e67205349562d414553",
			ad:         "00112233445566778899aabbccddeeffdeaddadadeaddadaffeeddccbbaa99887766554433221100",
			nonce:      "09f911029d74e35bd84156c5635688c0",
			ciphertext: "85825e22e90cf2ddda2c548dc7c1b6310dcdaca0cebf9dc6cb90583f5bf1506e02cd48832b00e4e598b2b22a53e6199d4df0c1666a35a0433b250dc134d776",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := fromHex(t, tc.key)
			pt := fromHex(t, tc.plaintext)
			ad := fromHex(t, tc.ad)
			nonce := fromHex(t, tc.nonce)
			expected := fromHex(t, tc.ciphertext)

			c, err := NewCMAC(k)
			if err != nil {
				t.Fatalf("NewCMAC failed: %v", err)
			}

			sealed := c.Seal(nil, nonce, pt, ad)
			if !bytes.Equal(sealed, expected) {
				t.Fatalf("Seal mismatch: got %x want %x", sealed, expected)
			}

			opened, err := c.Open(nil, nonce, sealed, ad)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			if !bytes.Equal(opened, pt) {
				t.Fatalf("Open mismatch: got %x want %x", opened, pt)
			}

			// Tamper test
			tampered := append([]byte(nil), sealed...)
			tampered[0] ^= 0x01
			if _, err := c.Open(nil, nonce, tampered, ad); err != ErrAuthFailed {
				t.Fatalf("expected ErrAuthFailed on tampered ciphertext, got %v", err)
			}
		})
	}
}

func TestAESGCMSIVVectors(t *testing.T) {
	// RFC 8452 Appendix C test vectors
	tests := []struct {
		name       string
		key        string
		plaintext  string
		ad         string
		nonce      string
		ciphertext string
	}{
		// AES-128
		{
			name:       "AES-128-empty",
			key:        "01000000000000000000000000000000",
			plaintext:  "",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "dc20e2d83f25705bb49e439eca56de25",
		},
		{
			name:       "AES-128-8bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "0100000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "b5d839330ac7b786578782fff6013b815b287c22493a364c",
		},
		{
			name:       "AES-128-12bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "010000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "7323ea61d05932260047d942a4978db357391a0bc4fdec8b0d106639",
		},
		{
			name:       "AES-128-16bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "01000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "743f7c8077ab25f8624e2e948579cf77303aaf90f6fe21199c6068577437a0c4",
		},
		{
			name:       "AES-128-32bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "0100000000000000000000000000000002000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "84e07e62ba83a6585417245d7ec413a9fe427d6315c09b57ce45f2e3936a94451a8e45dcd4578c667cd86847bf6155ff",
		},
		{
			name:       "AES-128-48bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "010000000000000000000000000000000200000000000000000000000000000003000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "3fd24ce1f5a67b75bf2351f181a475c7b800a5b4d3dcf70106b1eea82fa1d64df42bf7226122fa92e17a40eeaac1201b5e6e311dbf395d35b0fe39c2714388f8",
		},
		{
			name:       "AES-128-64bytes",
			key:        "01000000000000000000000000000000",
			plaintext:  "01000000000000000000000000000000020000000000000000000000000000000300000000000000000000000000000004000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "2433668f1058190f6d43e360f4f35cd8e475127cfca7028ea8ab5c20f7ab2af02516a2bdcbc08d521be37ff28c152bba36697f25b4cd169c6590d1dd39566d3f8a263dd317aa88d56bdf3936dba75bb8",
		},
		{
			name:       "AES-128-withAD",
			key:        "bde3b2f204d1e9f8b06bc47f9745b3d1",
			plaintext:  "6b3db4da3d57aa94842b9803a96e07fb6de7",
			ad:         "1860f762ebfbd08284e421702de0de18baa9c9596291b08466f37de21c7f",
			nonce:      "ae06556fb6aa7890bebc18fe",
			ciphertext: "6298b296e24e8cc35dce0bed484b7f30d5803e377094f04709f64d7b985310a4db84",
		},
		// AES-256
		{
			name:       "AES-256-empty",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "07f5f4169bbf55a8400cd47ea6fd400f",
		},
		{
			name:       "AES-256-8bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "0100000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "c2ef328e5c71c83b843122130f7364b761e0b97427e3df28",
		},
		{
			name:       "AES-256-12bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "010000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "9aab2aeb3faa0a34aea8e2b18ca50da9ae6559e48fd10f6e5c9ca17e",
		},
		{
			name:       "AES-256-16bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "01000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "85a01b63025ba19b7fd3ddfc033b3e76c9eac6fa700942702e90862383c6c366",
		},
		{
			name:       "AES-256-32bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "0100000000000000000000000000000002000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "4a6a9db4c8c6549201b9edb53006cba821ec9cf850948a7c86c68ac7539d027fe819e63abcd020b006a976397632eb5d",
		},
		{
			name:       "AES-256-48bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "010000000000000000000000000000000200000000000000000000000000000003000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "c00d121893a9fa603f48ccc1ca3c57ce7499245ea0046db16c53c7c66fe717e39cf6c748837b61f6ee3adcee17534ed5790bc96880a99ba804bd12c0e6a22cc4",
		},
		{
			name:       "AES-256-64bytes",
			key:        "0100000000000000000000000000000000000000000000000000000000000000",
			plaintext:  "01000000000000000000000000000000020000000000000000000000000000000300000000000000000000000000000004000000000000000000000000000000",
			ad:         "",
			nonce:      "030000000000000000000000",
			ciphertext: "c2d5160a1f8683834910acdafc41fbb1632d4a353e8b905ec9a5499ac34f96c7e1049eb080883891a4db8caaa1f99dd004d80487540735234e3744512c6f90ce112864c269fc0d9d88c61fa47e39aa08",
		},
		{
			name:       "AES-256-withAD",
			key:        "3c535de192eaed3822a2fbbe2ca9dfc88255e14a661b8aa82cc54236093bbc23",
			plaintext:  "ced532ce4159b035277d4dfbb7db62968b13cd4eec",
			ad:         "734320ccc9d9bbbb19cb81b2af4ecbc3e72834321f7aa0f70b7282b4f33df23f167541",
			nonce:      "688089e55540db1872504e1c",
			ciphertext: "626660c26ea6612fb17ad91e8e767639edd6c9faee9d6c7029675b89eaf4ba1ded1a286594",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := fromHex(t, tc.key)
			pt := fromHex(t, tc.plaintext)
			ad := fromHex(t, tc.ad)
			nonce := fromHex(t, tc.nonce)
			expected := fromHex(t, tc.ciphertext)

			c, err := NewGCM(k)
			if err != nil {
				t.Fatalf("NewGCM failed: %v", err)
			}

			sealed := c.Seal(nil, nonce, pt, ad)
			if !bytes.Equal(sealed, expected) {
				t.Fatalf("Seal mismatch: got %x want %x", sealed, expected)
			}

			opened, err := c.Open(nil, nonce, sealed, ad)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			if !bytes.Equal(opened, pt) {
				t.Fatalf("Open mismatch: got %x want %x", opened, pt)
			}

			// Tamper test
			tampered := append([]byte(nil), sealed...)
			tampered[0] ^= 0x01
			if _, err := c.Open(nil, nonce, tampered, ad); err != ErrAuthFailed {
				t.Fatalf("expected ErrAuthFailed on tampered ciphertext, got %v", err)
			}
		})
	}
}

func TestSivErrorsAndEdgeCases(t *testing.T) {
	// Invalid key sizes
	if _, err := NewCMAC(make([]byte, 16)); err != ErrInvalidKeySize {
		t.Fatalf("expected ErrInvalidKeySize for 16-byte key in NewCMAC, got %v", err)
	}
	if _, err := NewGCM(make([]byte, 24)); err != ErrInvalidKeySize {
		t.Fatalf("expected ErrInvalidKeySize for 24-byte key in NewGCM, got %v", err)
	}

	// Ciphertext too short
	c15, err := NewCMAC(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewCMAC failed: %v", err)
	}
	if _, err := c15.Open(nil, make([]byte, 16), make([]byte, 15), nil); err != ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed on short ciphertext, got %v", err)
	}

	c30, err := NewGCM(make([]byte, 16))
	if err != nil {
		t.Fatalf("NewGCM failed: %v", err)
	}
	if _, err := c30.Open(nil, make([]byte, 12), make([]byte, 15), nil); err != ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed on short ciphertext, got %v", err)
	}
}
