package nts

import (
	"bytes"
	"testing"
)

func TestReadServerResponse(t *testing.T) {
	proto := Record{Critical: true, Type: RecordNextProtocol, Body: []byte{0, 0}}
	aead := Record{Type: RecordAeadNegotiation, Body: []byte{0, 15}}
	cookie := Record{Type: RecordNewCookie, Body: bytes.Repeat([]byte{1}, 32)}
	eom := Record{Critical: true, Type: RecordEndOfMessage}
	for _, tc := range []struct {
		name    string
		records []Record
		valid   bool
	}{
		{"valid", []Record{proto, aead, cookie, eom}, true},
		{"large_cookie", []Record{proto, aead, {Type: RecordNewCookie, Body: bytes.Repeat([]byte{2}, 9000)}, eom}, true},
		{"warning_and_cookie", []Record{proto, aead, {Critical: true, Type: RecordWarning, Body: []byte{0, 0}}, cookie, eom}, false},
		{"unknown_optional", []Record{proto, aead, {Type: 1234}, cookie, eom}, true},
		{"missing_protocol", []Record{aead, cookie, eom}, false},
		{"duplicate_protocol", []Record{proto, proto, aead, cookie, eom}, false},
		{"missing_aead", []Record{proto, cookie, eom}, false},
		{"duplicate_aead", []Record{proto, aead, aead, cookie, eom}, false},
		{"unoffered_aead", []Record{proto, {Type: RecordAeadNegotiation, Body: []byte{0, 30}}, cookie, eom}, false},
		{"missing_cookie", []Record{proto, aead, eom}, false},
		{"empty_cookie", []Record{proto, aead, {Type: RecordNewCookie}, eom}, false},
		{"missing_eom", []Record{proto, aead, cookie}, false},
		{"noncritical_eom", []Record{proto, aead, cookie, {Type: RecordEndOfMessage}}, false},
		{"critical_unknown", []Record{proto, aead, cookie, {Critical: true, Type: 1234}, eom}, false},
		{"server_error", []Record{proto, aead, cookie, {Type: RecordError, Body: []byte{0, 0}}, eom}, false},
		{"oversized_record", []Record{proto, aead, {Type: RecordNewCookie, Body: bytes.Repeat([]byte{3}, 20000)}, eom}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire []byte
			for _, r := range tc.records {
				wire = append(wire, EncodeRecord(r)...)
			}
			got, err := ReadServerResponse(bytes.NewReader(wire), []uint16{15})
			if (err == nil) != tc.valid {
				t.Fatalf("response=%+v error=%v valid=%v", got, err, tc.valid)
			}
		})
	}
}
