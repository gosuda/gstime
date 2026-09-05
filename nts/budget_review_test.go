package nts_test

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/iotest"

	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/nts"
)

// These wire oracles intentionally do not use the production KE/extension
// encoders or request decoder. Numeric limits are independently derived from
// the IPv4 UDP ceiling and RFC 8915 framing, not copied from exported constants.
const reviewMaxUDP = 65535 - 20 - 8
const reviewPreferredUDP = 1280 - 40 - 8
const reviewMaxCookie = 65376 // 65504 - 48 header - 36 UID - 40 auth - 4 cookie header

func reviewRecord(kind uint16, body []byte) []byte {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint16(out, kind)
	binary.BigEndian.PutUint16(out[2:], uint16(len(body)))
	copy(out[4:], body)
	return out
}

func reviewKE(cookie []byte, optional []byte) []byte {
	out := reviewRecord(0x8001, []byte{0, 0})
	out = append(out, reviewRecord(4, []byte{0, 15})...)
	out = append(out, reviewRecord(5, cookie)...)
	out = append(out, optional...)
	return append(out, reviewRecord(0x8000, nil)...)
}

func reviewExtension(kind uint16, body []byte) []byte {
	length := (4 + len(body) + 3) &^ 3
	out := make([]byte, length)
	binary.BigEndian.PutUint16(out, kind)
	binary.BigEndian.PutUint16(out[2:], uint16(length))
	copy(out[4:], body)
	return out
}

func reviewAEAD(t *testing.T, id uint16) cipher.AEAD {
	t.Helper()
	keyLen := 32
	if id == 30 {
		keyLen = 16
	}
	a, err := nts.NewAEAD(id, bytes.Repeat([]byte{0x42}, keyLen))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func reviewCheckRequest(t *testing.T, header, ext, uid, cookie []byte, wantPlaceholders int, a cipher.AEAD) {
	t.Helper()
	if got := len(header) + len(ext); got > reviewMaxUDP {
		t.Fatalf("protected UDP payload is %d bytes, maximum %d", got, reviewMaxUDP)
	}
	var kinds []uint16
	var cookieWireLen, placeholders int
	for off := 0; off < len(ext); {
		if len(ext)-off < 4 {
			t.Fatal("truncated extension header")
		}
		kind, size := binary.BigEndian.Uint16(ext[off:]), int(binary.BigEndian.Uint16(ext[off+2:]))
		if size < 4 || size%4 != 0 || size > len(ext)-off {
			t.Fatalf("invalid extension length %d at offset %d", size, off)
		}
		body := ext[off+4 : off+size]
		kinds = append(kinds, kind)
		switch kind {
		case 0x0104:
			if off != 0 || len(body) != 32 || !bytes.Equal(body, uid) {
				t.Fatal("invalid unique ID")
			}
		case 0x0204:
			cookieWireLen = size
			if len(body) < len(cookie) || !bytes.Equal(body[:len(cookie)], cookie) || !bytes.Equal(body[len(cookie):], make([]byte, len(body)-len(cookie))) {
				t.Fatal("cookie changed on the protected wire")
			}
		case 0x0304:
			placeholders++
			if size != cookieWireLen || !bytes.Equal(body, make([]byte, len(body))) {
				t.Fatal("placeholder must match cookie length and contain only zeros")
			}
		case 0x0404:
			if off+size != len(ext) || len(body) < 4 {
				t.Fatal("authenticator is not the final field")
			}
			nlen, clen := int(binary.BigEndian.Uint16(body)), int(binary.BigEndian.Uint16(body[2:]))
			cipherOffset := 4 + (nlen+3)&^3
			if cipherOffset+clen > len(body) {
				t.Fatal("authenticator declares invalid lengths")
			}
			ad := append(append([]byte(nil), header...), ext[:off]...)
			plain, err := a.Open(nil, body[4:4+nlen], body[cipherOffset:cipherOffset+clen], ad)
			if err != nil || len(plain) != 0 {
				t.Fatalf("independent RFC authentication failed: plaintext=%x err=%v", plain, err)
			}
		default:
			t.Fatalf("unexpected field type %04x", kind)
		}
		off += size
	}
	if len(kinds) != 3+wantPlaceholders || kinds[0] != 0x0104 || kinds[1] != 0x0204 || kinds[len(kinds)-1] != 0x0404 || placeholders != wantPlaceholders {
		t.Fatalf("field sequence=%x; placeholders=%d, want %d", kinds, placeholders, wantPlaceholders)
	}
}

func TestReviewCookieToProtectedWire(t *testing.T) {
	for _, size := range []int{32, 512, 513, 9000, 20000, reviewMaxCookie - 1, reviewMaxCookie} {
		for _, id := range []uint16{15, 30} {
			t.Run(fmt.Sprintf("cookie_%d_aead_%d", size, id), func(t *testing.T) {
				cookie := bytes.Repeat([]byte{0xa5}, size)
				wire := reviewKE(cookie, nil)
				wire[11] = byte(id)
				response, err := nts.ReadServerResponse(bytes.NewReader(wire), []uint16{id})
				if err != nil {
					t.Fatal(err)
				}
				jar := nts.NewCookieJar()
				if added := jar.AddCookies(response.Cookies); added != 1 {
					t.Fatalf("parser accepted %d-byte cookie, but jar retained %d cookies", size, added)
				}
				managed, err := jar.AcquireForRequest(false)
				if err != nil {
					t.Fatal(err)
				}
				header := make([]byte, 48)
				header[0] = 0x23 // NTPv4 client.
				a := reviewAEAD(t, id)
				ext, uid, err := nts.BuildProtectedRequest(header, managed.Bytes, jar.CalculatePlaceholderCount(), a, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if size == 32 {
					want = 7
				} else if size <= 513 {
					want = 1
				}
				reviewCheckRequest(t, header, ext, uid, cookie, want, a)
				if size <= 513 && len(header)+len(ext) > reviewPreferredUDP {
					t.Fatalf("avoidable fragmentation: %d-byte payload", len(header)+len(ext))
				}
			})
		}
	}
}

func TestReviewRequestBudget(t *testing.T) {
	a := reviewAEAD(t, 15)
	for _, tc := range []struct {
		name         string
		cookieSize   int
		placeholders int
		want         int
	}{
		{"regression_72156_bytes", 9000, 7, 0},
		{"ordinary_full_inventory", 100, 7, 7},
		{"preferred_last_placeholder_fits", 548, 7, 1},
		{"preferred_next_placeholder_over", 549, 7, 0},
		{"preferred_limit_exact", 1104, 7, 0},
		{"excessive_placeholders", 32, 100, 7},
		{"integer_limit_placeholders", 32, int(^uint(0) >> 1), 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, cookie := make([]byte, 48), bytes.Repeat([]byte{1}, tc.cookieSize)
			ext, uid, err := nts.BuildProtectedRequest(header, cookie, tc.placeholders, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			reviewCheckRequest(t, header, ext, uid, cookie, tc.want, a)
		})
	}
	for _, tc := range []struct {
		name, reason string
		headerSize   int
		cookieSize   int
		placeholders int
	}{
		{"empty_cookie", "empty cookie", 48, 0, 0},
		{"cookie_over_budget", "unusable cookie", 48, reviewMaxCookie + 1, 0},
		{"extension_length_overflow", "16-bit overflow", 48, 65535, 0},
		{"short_header", "not a 48-byte header", 47, 32, 0},
		{"long_header", "extensions passed as header", 49, 32, 0},
		{"negative_placeholders", "negative placeholder count", 48, 32, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext, uid, err := nts.BuildProtectedRequest(make([]byte, tc.headerSize), make([]byte, tc.cookieSize), tc.placeholders, a, nil)
			if err == nil || ext != nil || uid != nil {
				t.Fatalf("must reject %s without output: ext=%d UID=%d error=%v", tc.reason, len(ext), len(uid), err)
			}
		})
	}
}

func TestReviewKEMessageAndCookieLimits(t *testing.T) {
	cookie := bytes.Repeat([]byte{1}, 32)
	base := reviewKE(cookie, nil)
	for _, size := range []int{20000, 65536} {
		t.Run(fmt.Sprintf("valid_message_%d", size), func(t *testing.T) {
			optional := reviewRecord(1234, bytes.Repeat([]byte{2}, size-len(base)-4))
			wire := reviewKE(cookie, optional)
			if len(wire) != size {
				t.Fatal("invalid oracle fixture length")
			}
			if _, err := nts.ReadServerResponse(iotest.OneByteReader(bytes.NewReader(wire)), []uint16{15}); err != nil {
				t.Fatalf("RFC 8915 section 4 permits large optional records within a 65536-byte response: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		wire    []byte
		wantErr error
	}{
		{"message_over_limit", reviewKE(cookie, reviewRecord(1234, make([]byte, 65537-len(base)-4))), nts.ErrRecordCapExceeded},
		{"cookie_over_limit", reviewKE(make([]byte, reviewMaxCookie+1), nil), nts.ErrInvalidCookie},
		{"unknown_critical_large_record", reviewKE(cookie, reviewRecord(0x8000|1234, make([]byte, 20000))), nts.ErrUnknownCritical},
		{"truncated_large_body", reviewRecord(1234, make([]byte, 20000))[:19999], io.ErrUnexpectedEOF},
		{"truncated_header", append(base[:len(base)-4], 0x80, 0), nts.ErrMissingEndOfMessage},
		{"invalid_end_body", append(base[:len(base)-4], reviewRecord(0x8000, []byte{0})...), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := nts.ReadServerResponse(bytes.NewReader(tc.wire), []uint16{15}); err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("invalid response: error=%v, want %v", err, tc.wantErr)
			}
		})
	}
	r := bytes.NewReader([]byte{0x04, 0xd2, 0xff, 0xff, 0xa5})
	if _, err := nts.ReadServerResponse(r, []uint16{15}); !errors.Is(err, nts.ErrRecordCapExceeded) || r.Len() != 1 {
		t.Fatalf("record exceeding total bound must fail before reading its body: remaining=%d error=%v", r.Len(), err)
	}
}

func TestReviewJarBounds(t *testing.T) {
	jar := nts.NewCookieJar()
	if got := jar.CalculatePlaceholderCount(); got > 7 {
		t.Fatalf("RFC 8915 recommends at most seven placeholders, got %d", got)
	}
	valid := bytes.Repeat([]byte{1}, reviewMaxCookie)
	if got := jar.AddCookies([][]byte{nil, make([]byte, reviewMaxCookie+1), valid}); got != 1 {
		t.Fatalf("valid largest cookie not retained: added %d", got)
	}
	valid[0] = 2
	managed, err := jar.AcquireForRequest(false)
	if err != nil || managed.Bytes[0] != 1 {
		t.Fatal("jar must retain an independent copy")
	}
	for range 20 {
		jar.AddCookies([][]byte{valid})
	}
	unused, inFlight, spent := jar.Counts()
	if unused+inFlight+spent != 8 {
		t.Fatalf("inventory is not bounded at eight: %d %d %d", unused, inFlight, spent)
	}
}

func reviewResponse(t *testing.T, a cipher.AEAD, plaintext []byte) ([]byte, []ntp.ExtensionField, []byte) {
	t.Helper()
	header, uid := make([]byte, 48), bytes.Repeat([]byte{3}, 32)
	header[0] = 0x24
	pre := reviewExtension(0x0104, uid)
	nonce := bytes.Repeat([]byte{4}, a.NonceSize())
	ad := append(append([]byte(nil), header...), pre...)
	sealed := a.Seal(nil, nonce, plaintext, ad)
	body := make([]byte, 4+len(nonce)+len(sealed))
	binary.BigEndian.PutUint16(body, uint16(len(nonce)))
	binary.BigEndian.PutUint16(body[2:], uint16(len(sealed)))
	copy(body[4:], nonce)
	copy(body[4+len(nonce):], sealed)
	wire := append(pre, reviewExtension(0x0404, body)...)
	fields, trailing, err := ntp.ParseExtensionFields(wire)
	if err != nil || len(trailing) != 0 {
		t.Fatalf("invalid independent response fixture: %v", err)
	}
	return header, fields, uid
}

func TestReviewResponseBudgetAndReplenishment(t *testing.T) {
	for _, id := range []uint16{15, 30} {
		for _, size := range []int{9000, reviewMaxCookie, reviewMaxCookie + 4} {
			t.Run(fmt.Sprintf("cookie_%d_aead_%d", size, id), func(t *testing.T) {
				a := reviewAEAD(t, id)
				cookie := bytes.Repeat([]byte{5}, size)
				header, fields, uid := reviewResponse(t, a, reviewExtension(0x0204, cookie))
				cookies, err := nts.VerifyAndDecryptResponse(header, fields, uid, a)
				if size > reviewMaxCookie {
					if err == nil {
						t.Fatal("accepted authenticated response exceeding packet or shared cookie budget")
					}
					return
				}
				if err != nil || len(cookies) != 1 || !bytes.Equal(cookies[0], cookie) {
					t.Fatalf("large replenishment cookie rejected: %v", err)
				}
				jar := nts.NewCookieJar()
				if jar.AddCookies(cookies) != 1 {
					t.Fatal("decrypted cookie lost by jar")
				}
				managed, err := jar.AcquireForRequest(false)
				if err != nil {
					t.Fatal(err)
				}
				ext, nextUID, err := nts.BuildProtectedRequest(header, managed.Bytes, 7, a, nil)
				if err != nil {
					t.Fatal(err)
				}
				reviewCheckRequest(t, header, ext, nextUID, cookie, 0, a)
			})
		}
	}
}

func TestReviewResponseMalformedCookies(t *testing.T) {
	a := reviewAEAD(t, 15)
	valid := reviewExtension(0x0204, bytes.Repeat([]byte{1}, 32))
	for _, tc := range []struct {
		name  string
		plain []byte
	}{
		{"empty_cookie", reviewExtension(0x0204, nil)},
		{"truncated_cookie", []byte{2, 4, 0, 12, 0, 0, 0, 0}},
		{"unaligned_field", []byte{2, 4, 0, 5, 0}},
		{"trailing_byte", append(append([]byte(nil), valid...), 0)},
		{"valid_then_empty_cookie", append(append([]byte(nil), valid...), reviewExtension(0x0204, nil)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, fields, uid := reviewResponse(t, a, tc.plain)
			cookies, err := nts.VerifyAndDecryptResponse(header, fields, uid, a)
			if err == nil || cookies != nil {
				t.Fatalf("malformed authenticated cookie fields must fail atomically: cookies=%d err=%v", len(cookies), err)
			}
		})
	}
}
