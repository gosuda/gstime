//go:build integration

package gstime_test

import (
	"context"
	"net"
	"testing"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

func TestRegressionNTPMustConvertUnixToGstTimescale(t *testing.T) {
	// Synthetic, explicitly configured history with one leap: this is not
	// presented as the complete real-world leap history.
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: 1483228800, Delta: 1}})
	if err != nil {
		t.Fatal(err)
	}
	label := core.UtcLabel{Date: core.GregorianDate{Year: 2024, Month: 1, Day: 1}, LeapHistoryID: lh.ID}
	wantGst, err := lh.UtcToGstInstant(label)
	if err != nil {
		t.Fatal(err)
	}
	wantUnix := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	cfg := config.DefaultConfig()
	for _, domain := range []string{"a", "b", "c"} {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			buf := make([]byte, 1024)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				done <- err
				return
			}
			req, err := ntp.ParsePacket(buf[:n])
			if err != nil {
				done <- err
				return
			}
			stamp := ntp.EncodeTimestamp(uint32(wantUnix/1_000_000_000+ntp.NtpToUnixOffset), 0)
			resp := ntp.Packet{Version: 4, Mode: 4, Stratum: 1,
				OrigTimestamp: req.TxTimestamp, RecvTimestamp: stamp, TxTimestamp: stamp}
			_, err = conn.WriteTo(resp.Encode(), peer)
			done <- err
		}()
		t.Cleanup(func() {
			_ = conn.Close()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
		cfg.Sources = append(cfg.Sources, config.SourceConfig{FaultDomainID: domain, Endpoint: conn.LocalAddr().String(), NTS: false})
	}
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	svc := gstime.NewClockService(raw, lh, id, cfg.Assurance.MaxWidthNs)
	// Keep local estimates and raw time fixed to isolate protocol conversion
	// from the separate sample-reference-time issue.
	if err := svc.InitializeEstimate(raw.Read().Raw, wantGst, 0); err != nil {
		t.Fatal(err)
	}
	engine, err := gstime.NewSyncEngine(cfg, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := svc.Now()
	proj, err := svc.NowUnixProjection()
	if err != nil {
		t.Fatal(err)
	}
	_, _, utc, _, err := svc.NowUtc(lh.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s interval=%+v wantGst=%d gotUnix=%d wantUnix=%d utc=%v", now.Status, now.Interval, wantGst, proj.Nanos, wantUnix, utc)
	if now.Interval == nil || now.Interval.Earliest > wantGst || now.Interval.Latest < wantGst {
		t.Error("certified GstInstant interval excludes the configured leap-aware instant")
	}
	// Common-reference propagation includes raw read uncertainty, so the
	// selected center may move by sub-microsecond rounding. This test detects
	// a missing SI second, not exact centering inside a valid interval.
	if delta := int64(proj.Nanos) - wantUnix; delta < -1_000 || delta > 1_000 {
		t.Errorf("Unix projection error: %d ns", delta)
	}
}
