package nts

import (
	"bytes"
	"crypto/cipher"
	"testing"

	"gosuda.org/gstime/ntp"
)

func TestNtsKeRecordFramingByteAligned(t *testing.T) {
	// NTS-KE record bodies are byte strings and not forced to 4-octet alignment
	bodyOdd := []byte("1234567") // 7 bytes, odd length
	rec := Record{
		Critical: false,
		Type:     RecordNewCookie,
		Body:     bodyOdd,
	}
	encoded := EncodeRecord(rec)
	if len(encoded) != 4+7 {
		t.Fatalf("expected length 11, got %d", len(encoded))
	}

	parsed, err := ParseRecords(encoded, 1024)
	if err != nil {
		t.Fatalf("ParseRecords failed: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 record, got %d", len(parsed))
	}
	if parsed[0].Critical || parsed[0].Type != RecordNewCookie || !bytes.Equal(parsed[0].Body, bodyOdd) {
		t.Fatalf("record mismatch: %+v", parsed[0])
	}
}

func TestNtsKeWarningWithCriticalBit(t *testing.T) {
	warningRec := Record{
		Critical: true,
		Type:     RecordWarning,
		Body:     []byte{0, 1},
	}
	encoded := EncodeRecord(warningRec)
	parsed, err := ParseRecords(encoded, 1024)
	if err != nil {
		t.Fatalf("ParseRecords failed: %v", err)
	}
	if len(parsed) != 1 || !parsed[0].Critical || parsed[0].Type != RecordWarning {
		t.Fatalf("warning record mismatch")
	}
}

func TestTlsExporterContext(t *testing.T) {
	// Context format: uint16_be(nextProto) || uint16_be(aeadID) || uint8(direction)
	ctxC2S := BuildExporterContext(0, 15, 0)
	expectedC2S := []byte{0x00, 0x00, 0x00, 0x0f, 0x00}
	if !bytes.Equal(ctxC2S, expectedC2S) {
		t.Fatalf("C2S context mismatch: got %x want %x", ctxC2S, expectedC2S)
	}

	ctxS2C := BuildExporterContext(0, 30, 1)
	expectedS2C := []byte{0x00, 0x00, 0x00, 0x1e, 0x01}
	if !bytes.Equal(ctxS2C, expectedS2C) {
		t.Fatalf("S2C context mismatch: got %x want %x", ctxS2C, expectedS2C)
	}
}

func mockExporter(keyMaterial []byte) KeyExporter {
	return func(label string, context []byte, length int) ([]byte, error) {
		if label != ExporterLabel {
			return nil, ErrNtsKeFailed
		}
		out := make([]byte, length)
		copy(out, keyMaterial)
		// Mix in context
		for i, b := range context {
			out[i%length] ^= b
		}
		return out, nil
	}
}

func TestAead15And30Encryption(t *testing.T) {
	mockMat := bytes.Repeat([]byte{0x42}, 64)
	exporter := mockExporter(mockMat)

	// Test AEAD 15 (AES-SIV-CMAC-256)
	c2s15, s2c15, err := ExportDirectionalKeys(exporter, 0, 15)
	if err != nil {
		t.Fatalf("export AEAD 15 keys: %v", err)
	}
	if len(c2s15) != 32 || len(s2c15) != 32 {
		t.Fatalf("key lengths for AEAD 15 must be 32, got %d, %d", len(c2s15), len(s2c15))
	}

	aead15, err := NewAEAD(15, c2s15)
	if err != nil {
		t.Fatalf("NewAEAD(15) failed: %v", err)
	}

	// Test AEAD 30 (AES-128-GCM-SIV)
	c2s30, s2c30, err := ExportDirectionalKeys(exporter, 0, 30)
	if err != nil {
		t.Fatalf("export AEAD 30 keys: %v", err)
	}
	if len(c2s30) != 16 || len(s2c30) != 16 {
		t.Fatalf("key lengths for AEAD 30 must be 16, got %d, %d", len(c2s30), len(s2c30))
	}

	aead30, err := NewAEAD(30, c2s30)
	if err != nil {
		t.Fatalf("NewAEAD(30) failed: %v", err)
	}
	if aead30.NonceSize() != 12 {
		t.Fatalf("AEAD 30 nonce size must be 12, got %d", aead30.NonceSize())
	}

	// Test round-trip with AEAD 15 and AEAD 30
	testAeadRoundTrip(t, aead15)
	testAeadRoundTrip(t, aead30)
}

func testAeadRoundTrip(t *testing.T, a cipher.AEAD) {
	nonce := make([]byte, a.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	plaintext := []byte("inner-extension-field-payload")
	ad := []byte("ntp-header-and-preceding-extension-fields")

	sealed := a.Seal(nil, nonce, plaintext, ad)
	opened, err := a.Open(nil, nonce, sealed, ad)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("plaintext mismatch: got %q want %q", opened, plaintext)
	}

	// Tamper test
	sealed[0] ^= 0xff
	if _, err := a.Open(nil, nonce, sealed, ad); err == nil {
		t.Fatalf("expected error on tampered ciphertext")
	}
}

func TestStrictAuthenticatorLastRejection(t *testing.T) {
	// Create response where Authenticator is NOT last (e.g. followed by another extension field)
	ntpHeader := make([]byte, 48)
	fields := []ntp.ExtensionField{
		{Type: ExtTypeUniqueID, Value: []byte("unique-id-32-bytes-test-id-12345")},
		{Type: ExtTypeAuthenticator, Wire: make([]byte, 28)},
		{Type: 0x9999, Value: []byte("trailing-unauthenticated-field")}, // Trailing!
	}

	mockAead, _ := NewAEAD(15, bytes.Repeat([]byte{1}, 32))
	_, err := VerifyAndDecryptResponse(ntpHeader, fields, []byte("unique-id-32-bytes-test-id-12345"), mockAead)
	if err != ErrAuthNotLast {
		t.Fatalf("expected ErrAuthNotLast, got %v", err)
	}
}

func TestCookieLifecycleAndBurstReserve(t *testing.T) {
	jar := NewCookieJar()

	// Add 8 cookies
	var initialCookies [][]byte
	for i := 0; i < 8; i++ {
		initialCookies = append(initialCookies, bytes.Repeat([]byte{byte(i + 1)}, 32))
	}
	added := jar.AddCookies(initialCookies)
	if added != 8 {
		t.Fatalf("expected 8 added, got %d", added)
	}

	unused, inFlight, spent := jar.Counts()
	if unused != 8 || inFlight != 0 || spent != 0 {
		t.Fatalf("counts mismatch: %d %d %d", unused, inFlight, spent)
	}

	// Normal poll acquires 1 cookie (now 7 unused, 1 in flight)
	c1, err := jar.AcquireForRequest(false)
	if err != nil {
		t.Fatalf("acquire c1 failed: %v", err)
	}

	// Pipeline 4 requests as burst
	for i := 0; i < 4; i++ {
		_, err := jar.AcquireForRequest(true)
		if err != nil {
			t.Fatalf("burst acquire %d failed: %v", i, err)
		}
	}
	// Currently 3 unused, 5 in flight.
	// Acquire 1 more burst: leaves 2 unused.
	_, err = jar.AcquireForRequest(true)
	if err != nil {
		t.Fatalf("burst acquire failed: %v", err)
	}
	// Currently 2 unused (at low watermark!).
	if !jar.NeedsReplenishment() {
		t.Fatalf("expected NeedsReplenishment true when unused <= 2")
	}

	// Burst reserve = 1.
	// Acquire 1 more burst: leaves 1 unused.
	_, err = jar.AcquireForRequest(true)
	if err != nil {
		t.Fatalf("burst acquire failed: %v", err)
	}
	// Now unused == 1 (== burstReserve).
	// Extra burst acquire MUST pause (fail with starvation) to preserve reserve for normal poll!
	_, err = jar.AcquireForRequest(true)
	if err != ErrCookieStarvation {
		t.Fatalf("expected burst pause due to reserve, got %v", err)
	}

	// Normal poll CAN consume the last cookie in reserve
	lastCookie, err := jar.AcquireForRequest(false)
	if err != nil {
		t.Fatalf("normal poll should consume reserve cookie: %v", err)
	}

	// Mark c1 spent
	jar.MarkSpent(c1.ID)
	jar.MarkSpent(lastCookie.ID)

	_, _, spent = jar.Counts()
	if spent != 2 {
		t.Fatalf("expected 2 spent, got %d", spent)
	}
}
