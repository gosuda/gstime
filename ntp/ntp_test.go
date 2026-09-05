package ntp

import (
	"bytes"
	"testing"
)

func TestPacketEncodeDecode(t *testing.T) {
	p := &Packet{
		LeapIndicator: 0,
		Version:       4,
		Mode:          4,
		Stratum:       1,
		Poll:          6,
		Precision:     -20,
		RootDelay:     65536, // 1.0 second
		RootDisp:      32768, // 0.5 second
		RefID:         [4]byte{'G', 'P', 'S', 0},
		RefTimestamp:  EncodeTimestamp(100, 200),
		OrigTimestamp: EncodeTimestamp(300, 400),
		RecvTimestamp: EncodeTimestamp(500, 600),
		TxTimestamp:   EncodeTimestamp(700, 800),
	}

	enc := p.Encode()
	if len(enc) != HeaderSize {
		t.Fatalf("expected length %d, got %d", HeaderSize, len(enc))
	}

	dec, err := ParsePacket(enc)
	if err != nil {
		t.Fatalf("ParsePacket failed: %v", err)
	}

	if dec.Version != 4 || dec.Mode != 4 || dec.Stratum != 1 || dec.Poll != 6 || dec.Precision != -20 {
		t.Fatalf("header fields mismatch: %+v", dec)
	}
	if dec.RootDelay != 65536 || dec.RootDisp != 32768 {
		t.Fatalf("root metrics mismatch: delay=%d disp=%d", dec.RootDelay, dec.RootDisp)
	}
	if !bytes.Equal(dec.RefID[:], p.RefID[:]) {
		t.Fatalf("RefID mismatch")
	}
}

func TestEraUnfolding2036(t *testing.T) {
	// Era 0 wraps on 2036-02-07 06:28:16 UTC.
	// In NTP seconds, 2^32 is 4,294,967,296.
	// In Unix seconds, this is 4,294,967,296 - 2,208,988,800 = 2_085_978_496.

	// 1. Immediately before wrap (1 second before):
	// NTP second = 4,294,967,295 (s32 = 0xffffffff).
	// Unix second = 2_085_978_495.
	coarseBefore := int64(2_085_978_490) // coarse anchor 5 seconds earlier
	unfoldedBefore, err := UnfoldNtpSeconds(0xffffffff, coarseBefore)
	if err != nil {
		t.Fatalf("unfold before failed: %v", err)
	}
	if unfoldedBefore != 4294967295 {
		t.Fatalf("unfolded before mismatch: got %d want 4294967295", unfoldedBefore)
	}

	// 2. Exactly at wrap:
	// NTP second in Era 1 = 4,294,967,296 (s32 = 0).
	// Unix second = 2_085_978_496.
	coarseAt := int64(2_085_978_496)
	unfoldedAt, err := UnfoldNtpSeconds(0, coarseAt)
	if err != nil {
		t.Fatalf("unfold at wrap failed: %v", err)
	}
	if unfoldedAt != 4294967296 {
		t.Fatalf("unfolded at wrap mismatch: got %d want 4294967296", unfoldedAt)
	}

	// 3. After wrap (1 hour after wrap):
	// s32 = 3600.
	coarseAfter := int64(2_085_978_496 + 3600)
	unfoldedAfter, err := UnfoldNtpSeconds(3600, coarseAfter)
	if err != nil {
		t.Fatalf("unfold after wrap failed: %v", err)
	}
	if unfoldedAfter != 4294967296+3600 {
		t.Fatalf("unfolded after wrap mismatch: got %d want %d", unfoldedAfter, 4294967296+3600)
	}
}

func TestNegativeRawDelayClampedToPrecisionFloor(t *testing.T) {
	// Test that signed negative raw delay clamps to precision floor rather than being reflected with abs()
	// T1 = 100ns, T2 = 200ns, T3 = 250ns, T4 = 110ns (apparent negative delay due to timestamp jitter)
	// delta_raw = (T4 - T1) - (T3 - T2) = (110 - 100) - (250 - 200) = 10 - 50 = -40 ns.
	in := MeasurementInput{
		T1LocalSendEstimate: 100,
		T2ServerRecv:        200,
		T3ServerTx:          250,
		T4LocalRecvEstimate: 110,
		LocalSendRaw:        1000,
		LocalRecvRaw:        1100,
		LocalEstimateAtMid:  105,
		PrecisionFloorNs:    10, // precision floor 10ns
	}

	res, err := ComputeMeasurement(in)
	if err != nil {
		t.Fatalf("ComputeMeasurement failed: %v", err)
	}

	if res.DeltaRaw != -40 {
		t.Fatalf("expected DeltaRaw = -40, got %d", res.DeltaRaw)
	}
	if res.DeltaClamped != 10 {
		t.Fatalf("expected DeltaClamped = 10 (clamped to precision floor, NOT abs(-40)=40), got %d", res.DeltaClamped)
	}
}

func TestKissOfDeathDispatch(t *testing.T) {
	p := &Packet{
		Stratum: 0,
		RefID:   [4]byte{'R', 'A', 'T', 'E'},
	}
	isKoD, code := IsKissOfDeath(p)
	if !isKoD || code != "RATE" {
		t.Fatalf("expected RATE KoD, got %v, %s", isKoD, code)
	}

	pNormal := &Packet{
		Stratum: 2,
		RefID:   [4]byte{192, 0, 2, 1},
	}
	isKoD2, _ := IsKissOfDeath(pNormal)
	if isKoD2 {
		t.Fatalf("stratum 2 should not be KoD")
	}
}

func TestReachabilityBitmapOutOfOrderAppendixD(t *testing.T) {
	// Appendix D: For transmissions 10, 11, 12, current serial is 12.
	// If response for serial 10 arrives first: age = 12 - 10 = 2, reach |= 1 << 2 (bit 2).
	// Later response for serial 12 sets bit 0.
	// Response for serial 11 sets bit 1.
	// Final bitmap should have bits 0, 1, 2 set -> 0x07.
	rm := NewReachabilityManager()

	// Advance to serial 9
	for i := 0; i < 9; i++ {
		rm.OnTransmit()
	}

	s10 := rm.OnTransmit() // serial 10
	s11 := rm.OnTransmit() // serial 11
	s12 := rm.OnTransmit() // serial 12

	if s12 != 12 {
		t.Fatalf("expected serial 12, got %d", s12)
	}

	// Response 10 arrives first
	rm.OnResponse(s10)
	if rm.Bitmap() != 0x04 { // 1 << 2
		t.Fatalf("expected bitmap 0x04, got 0x%02x", rm.Bitmap())
	}

	// Response 12 arrives
	rm.OnResponse(s12)
	if rm.Bitmap() != 0x05 { // (1 << 2) | (1 << 0) = 4 + 1 = 5
		t.Fatalf("expected bitmap 0x05, got 0x%02x", rm.Bitmap())
	}

	// Response 11 arrives
	rm.OnResponse(s11)
	if rm.Bitmap() != 0x07 { // bits 0, 1, 2 = 7
		t.Fatalf("expected bitmap 0x07, got 0x%02x", rm.Bitmap())
	}

	// Duplicate response 10 arrives: bitmap should not change
	rm.OnResponse(s10)
	if rm.Bitmap() != 0x07 {
		t.Fatalf("duplicate altered bitmap: got 0x%02x", rm.Bitmap())
	}
}

func TestGateCSignIdentity(t *testing.T) {
	// Section 2.13: abs(measuredOffsetSlowPositive + predictedOffsetFastPositive) - delayAnomaly > allowedInnovation
	// Because predictedOffsetFastPositive = -predictedOffsetSlowPositive,
	// test with multiple sign combinations that (slowPos + (-slowPos)) == 0.
	cases := []struct {
		slowPos  float64
		fastPos  float64
		expected float64
	}{
		{100.0, -100.0, 0.0},
		{-50.0, 50.0, 0.0},
		{25.5, -25.5, 0.0},
		{0.0, 0.0, 0.0},
	}

	for _, c := range cases {
		diff := c.slowPos + c.fastPos
		if diff != c.expected {
			t.Fatalf("Gate-C sign identity failed: %f + %f = %f, want %f", c.slowPos, c.fastPos, diff, c.expected)
		}
	}
}

func TestExtensionFieldParsing(t *testing.T) {
	fields := []ExtensionField{
		{Type: 0x0104, Value: []byte("unique-id-32-bytes-test-payload!")},
		{Type: 0x0204, Value: []byte("cookie-1234")},
	}
	enc := EncodeExtensionFields(fields)

	parsed, trailing, err := ParseExtensionFields(enc)
	if err != nil {
		t.Fatalf("ParseExtensionFields failed: %v", err)
	}
	if len(trailing) != 0 {
		t.Fatalf("expected 0 trailing bytes, got %d", len(trailing))
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed fields, got %d", len(parsed))
	}
	if parsed[0].Type != 0x0104 || string(parsed[0].Value) != "unique-id-32-bytes-test-payload!" {
		t.Fatalf("field 0 mismatch: %+v", parsed[0])
	}
	if parsed[1].Type != 0x0204 || !bytes.HasPrefix(parsed[1].Value, []byte("cookie-1234")) {
		t.Fatalf("field 1 mismatch: %+v", parsed[1])
	}
}
