//go:build integration

package gstime

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"gosuda.org/gstime/nts"
)

var regressionNTSRootsOnce sync.Once
var regressionNTSCert tls.Certificate
var regressionNTSCertErr error

// The trust override is scoped to this test process. No certificate or private
// key is written to disk or installed in the OS trust store.
func regressionNTSCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",x509usefallbackroots=1")
	regressionNTSRootsOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			regressionNTSCertErr = err
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gstime loopback test"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
			IsCA:        true, BasicConstraintsValid: true,
			KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			regressionNTSCertErr = err
			return
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			regressionNTSCertErr = err
			return
		}
		pool := x509.NewCertPool()
		pool.AddCert(cert)
		x509.SetFallbackRoots(pool)
		regressionNTSCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	if regressionNTSCertErr != nil {
		t.Fatal(regressionNTSCertErr)
	}
	return regressionNTSCert
}

func TestRegressionNTSKEIntegration(t *testing.T) {
	cert := regressionNTSCertificate(t)
	proto := nts.Record{Critical: true, Type: nts.RecordNextProtocol, Body: []byte{0, 0}}
	aead := nts.Record{Critical: true, Type: nts.RecordAeadNegotiation, Body: []byte{0, 15}}
	cookie := nts.Record{Type: nts.RecordNewCookie, Body: bytes.Repeat([]byte{0x42}, 32)}
	warning := nts.Record{Critical: true, Type: nts.RecordWarning, Body: []byte{0, 0}}
	eom := nts.Record{Critical: true, Type: nts.RecordEndOfMessage}
	for _, tc := range []struct {
		name        string
		records     []nts.Record
		fragment    bool
		version     uint16
		wantSuccess bool
	}{
		{"valid_type5_cookie", []nts.Record{proto, aead, cookie, eom}, false, tls.VersionTLS13, true},
		{"fragmented_valid_response", []nts.Record{proto, aead, cookie, eom}, true, tls.VersionTLS13, true},
		{"warning_is_not_a_cookie", []nts.Record{proto, aead, warning, eom}, false, tls.VersionTLS13, false},
		{"error_record_must_abort", []nts.Record{proto, aead, cookie, {Critical: true, Type: nts.RecordError, Body: []byte{0, 2}}, eom}, false, tls.VersionTLS13, false},
		{"unknown_critical_must_abort", []nts.Record{proto, aead, cookie, {Critical: true, Type: 1234}, eom}, false, tls.VersionTLS13, false},
		{"missing_end_of_message", []nts.Record{proto, aead, cookie}, false, tls.VersionTLS13, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
				Certificates: []tls.Certificate{cert}, NextProtos: []string{"ntske/1"},
				MinVersion: tc.version, MaxVersion: tc.version,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			type serverResult struct {
				err error
				key []byte
			}
			done := make(chan serverResult, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					done <- serverResult{err: err}
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				request := make([]byte, len(nts.BuildClientRequest([]uint16{15, 30}, true)))
				if _, err = io.ReadFull(c, request); err != nil {
					done <- serverResult{err: err}
					return
				}
				state := c.(*tls.Conn).ConnectionState()
				key, err := state.ExportKeyingMaterial(nts.ExporterLabel, nts.BuildExporterContext(0, 15, 0), 32)
				if err != nil {
					done <- serverResult{err: err}
					return
				}
				var response []byte
				for _, r := range tc.records {
					response = append(response, nts.EncodeRecord(r)...)
				}
				if tc.fragment {
					// Separate TLS writes guarantee the first application read may
					// return only two bytes of the first four-byte NTS-KE header.
					_, err = c.Write(response[:2])
					if err == nil {
						_, err = c.Write(response[2:])
					}
				} else {
					_, err = c.Write(response)
				}
				done <- serverResult{err: err, key: key}
			}()
			q := &defaultSourceQuerier{timeout: 2 * time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			sess, err := q.negotiateNTSKE(ctx, ln.Addr().String())
			server := <-done
			if server.err != nil {
				t.Fatalf("loopback server failure: %v", server.err)
			}
			t.Logf("session_created=%v error=%v", sess != nil, err)
			if sess != nil && !bytes.Equal(sess.c2sKey, server.key) {
				t.Fatal("TLS exporter mismatch in test control")
			}
			if tc.wantSuccess && err != nil {
				t.Fatalf("valid NTS-KE response rejected: %v", err)
			}
			if !tc.wantSuccess && err == nil {
				t.Fatal("invalid NTS-KE response/transport accepted")
			}
		})
	}
}
