//go:build integration

package gstime

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/nts"
)

// This peer follows RFC 8915's AD boundary independently of the packet helpers
// under test. No public NTP/NTS endpoint or operating-system trust change is used.
func TestNTSEndToEndNegotiatedPort(t *testing.T) {
	for _, cookieSize := range []int{32, 9000} {
		t.Run(fmt.Sprintf("cookie_%d", cookieSize), func(t *testing.T) { testNTSEndToEndNegotiatedPort(t, cookieSize) })
	}
}

func testNTSEndToEndNegotiatedPort(t *testing.T, cookieSize int) {
	cert := regressionNTSCertificate(t)
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	port := udp.LocalAddr().(*net.UDPAddr).Port
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"ntske/1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	keys := make(chan [2][]byte, 1)
	serverErrors := make(chan error, 2)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		request := make([]byte, len(nts.BuildClientRequest([]uint16{15, 30}, true)))
		if _, err = io.ReadFull(conn, request); err != nil {
			serverErrors <- err
			return
		}
		state := conn.(*tls.Conn).ConnectionState()
		c2s, s2c, err := nts.ExportDirectionalKeys(state.ExportKeyingMaterial, 0, 15)
		if err != nil {
			serverErrors <- err
			return
		}
		keys <- [2][]byte{c2s, s2c}
		var portBytes [2]byte
		binary.BigEndian.PutUint16(portBytes[:], uint16(port))
		var response []byte
		for _, r := range []nts.Record{
			{Critical: true, Type: nts.RecordNextProtocol, Body: []byte{0, 0}},
			{Type: nts.RecordAeadNegotiation, Body: []byte{0, 15}},
			{Type: nts.RecordNewCookie, Body: bytes.Repeat([]byte{1}, cookieSize)},
			{Type: nts.RecordServerNegotiation, Body: []byte("127.0.0.1")},
			{Type: nts.RecordPortNegotiation, Body: portBytes[:]},
			{Critical: true, Type: nts.RecordEndOfMessage},
		} {
			response = append(response, nts.EncodeRecord(r)...)
		}
		_, err = conn.Write(response)
		serverErrors <- err
	}()
	const unixSeconds int64 = 1704067200
	go func() {
		_ = udp.SetDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 65536)
		n, peer, err := udp.ReadFrom(buf)
		if err != nil {
			serverErrors <- err
			return
		}
		var material [2][]byte
		select {
		case material = <-keys:
		case <-time.After(3 * time.Second):
			serverErrors <- fmt.Errorf("missing TLS exporter keys")
			return
		}
		c2s, err := nts.NewAEAD(15, material[0])
		if err != nil {
			serverErrors <- err
			return
		}
		s2c, err := nts.NewAEAD(15, material[1])
		if err != nil {
			serverErrors <- err
			return
		}
		if n < 48 {
			serverErrors <- fmt.Errorf("short client packet")
			return
		}
		fields, _, err := ntp.ParseExtensionFields(buf[48:n])
		if err != nil {
			serverErrors <- err
			return
		}
		auth := fields[len(fields)-1].Wire
		nonceLen, cipherLen := int(binary.BigEndian.Uint16(auth[4:6])), int(binary.BigEndian.Uint16(auth[6:8]))
		if _, err = c2s.Open(nil, auth[8:8+nonceLen], auth[8+nonceLen:8+nonceLen+cipherLen], buf[:n-len(auth)]); err != nil {
			serverErrors <- fmt.Errorf("RFC client authentication: %w", err)
			return
		}
		stamp := ntp.EncodeTimestamp(uint32(unixSeconds+ntp.NtpToUnixOffset), 0)
		header := (&ntp.Packet{Version: 4, Mode: 4, Stratum: 1, RecvTimestamp: stamp, TxTimestamp: stamp}).Encode()
		pre := ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: nts.ExtTypeUniqueID, Value: fields[0].Value}})
		ad := append(append([]byte{}, header...), pre...)
		nonce := bytes.Repeat([]byte{9}, s2c.NonceSize()) // single response under this ephemeral key
		plain := ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: nts.ExtTypeCookie, Value: bytes.Repeat([]byte{2}, cookieSize)}})
		encrypted := s2c.Seal(nil, nonce, plain, ad)
		body := make([]byte, 4+len(nonce)+len(encrypted))
		binary.BigEndian.PutUint16(body[:2], uint16(len(nonce)))
		binary.BigEndian.PutUint16(body[2:4], uint16(len(encrypted)))
		copy(body[4:], nonce)
		copy(body[4+len(nonce):], encrypted)
		packet := append(ad, ntp.EncodeExtensionFields([]ntp.ExtensionField{{Type: nts.ExtTypeAuthenticator, Value: body}})...)
		_, err = udp.WriteTo(packet, peer)
		serverErrors <- err
	}()
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	raw.SetScaleEnvelope(core.RateScale(core.OneQ48), core.RateScale(core.OneQ48))
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: 1483228800, Delta: 1}})
	cfg := config.DefaultConfig()
	cfg.Assurance.FaultBudget = 0
	cfg.Assurance.MinVotingDomains = 1
	cfg.Assurance.MinHonestCoverage = 1
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "loopback", Endpoint: listener.Addr().String(), NTS: true}}
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	svc := NewClockService(raw, lh, id, cfg.Assurance.MaxWidthNs)
	want := core.GstInstant((unixSeconds + 1) * 1_000_000_000)
	if err = svc.InitializeEstimate(raw.Read().Raw, want, 0); err != nil {
		t.Fatal(err)
	}
	engine, err := NewSyncEngine(cfg, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err = engine.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
	now := svc.Now()
	if now.Status != StatusSynced || now.Interval == nil || now.Interval.Earliest > want || now.Interval.Latest < want {
		t.Fatalf("incorrect NTS-certified interval: %+v", now)
	}
	session := engine.querier.(*defaultSourceQuerier).ntsState[cfg.Sources[0].Endpoint]
	unused, inFlight, _ := session.jar.Counts()
	if unused != 1 || inFlight != 0 {
		t.Fatalf("cookie lifecycle unused=%d inFlight=%d", unused, inFlight)
	}
}
