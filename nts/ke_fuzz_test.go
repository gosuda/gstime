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
	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := ReadServerResponse(bytes.NewReader(data), []uint16{15, 30})
		if err == nil && (result == nil || len(result.Cookies) == 0 || result.Port == 0) {
			t.Fatal("accepted incomplete key exchange")
		}
	})
}
