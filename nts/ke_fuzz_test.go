package nts

import (
	"bytes"
	"testing"
)

func FuzzReadServerResponse(f *testing.F) {
	var valid []byte
	for _, r := range []Record{
		{Critical: true, Type: RecordNextProtocol, Body: []byte{0, 0}},
		{Type: RecordAeadNegotiation, Body: []byte{0, 15}},
		{Type: RecordNewCookie, Body: bytes.Repeat([]byte{1}, 32)},
		{Critical: true, Type: RecordEndOfMessage},
	} {
		valid = append(valid, EncodeRecord(r)...)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add(valid[:3])
	for _, size := range []int{513, 9000, 20000, MaxCookieSize, MaxCookieSize + 1} {
		seed := append([]byte(nil), valid[:12]...)
		seed = append(seed, EncodeRecord(Record{Type: RecordNewCookie, Body: bytes.Repeat([]byte{1}, size)})...)
		seed = append(seed, EncodeRecord(Record{Critical: true, Type: RecordEndOfMessage})...)
		f.Add(seed)
	}
	largeOptional := append([]byte(nil), valid[:len(valid)-4]...)
	largeOptional = append(largeOptional, EncodeRecord(Record{Type: 1234, Body: make([]byte, MaxKEMessageSize-len(valid)-4)})...)
	largeOptional = append(largeOptional, valid[len(valid)-4:]...)
	f.Add(largeOptional)
	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := ReadServerResponse(bytes.NewReader(data), []uint16{15, 30})
		if err != nil {
			return
		}
		if result == nil || len(result.Cookies) == 0 || result.Port == 0 {
			t.Fatal("accepted incomplete key exchange")
		}
		for _, cookie := range result.Cookies {
			if !validCookieSize(len(cookie)) {
				t.Fatal("accepted an unusable cookie")
			}
		}
		if got := NewCookieJar().AddCookies(result.Cookies); got != min(8, len(result.Cookies)) {
			t.Fatalf("parser/jar acceptance mismatch: retained %d of %d", got, len(result.Cookies))
		}
	})
}
