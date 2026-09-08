package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

func TestRunReturnsUpstreamTime(t *testing.T) {
	target := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	endpoint, _ := startNTPPeer(t, func(req *ntp.Packet) *ntp.Packet {
		stamp := ntp.EncodeTimestamp(uint32(target.Unix()+2_208_988_800), 0)
		return &ntp.Packet{
			Version: 4, Mode: 4, Stratum: 1, Precision: -20,
			RootDisp:      4 * 65536,
			OrigTimestamp: req.TxTimestamp, RecvTimestamp: stamp, TxTimestamp: stamp,
		}
	})
	args, lh := writeInputs(t, endpoint)

	utc, err := run(context.Background(), args)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if utc.Status != gstime.StatusSynced || !utc.HasInterval || !utc.HasEstimate {
		t.Fatalf("upstream round returned status=%s interval=%t estimate=%t", utc.Status, utc.HasInterval, utc.HasEstimate)
	}
	if utc.Estimate.Date != (core.GregorianDate{Year: 2025, Month: 1, Day: 2}) {
		t.Fatalf("estimate %s did not follow upstream time %s", utc.Estimate, target)
	}
	lo, err := lh.UtcToGstInstant(utc.Earliest)
	if err != nil {
		t.Fatal(err)
	}
	hi, err := lh.UtcToGstInstant(utc.Latest)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := lh.UnixNanosToGstInstant(core.UnixNanos(target.UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if lo > upstream || hi < upstream {
		t.Fatalf("UTC interval [%s, %s] excludes upstream %s", utc.Earliest, utc.Latest, target)
	}
}

func TestRunRejectsFailedUpstream(t *testing.T) {
	endpoint, _ := startNTPPeer(t, func(req *ntp.Packet) *ntp.Packet {
		return &ntp.Packet{
			Version: 4, Mode: 4, Stratum: 0,
			OrigTimestamp: req.TxTimestamp, RefID: [4]byte{'R', 'A', 'T', 'E'},
		}
	})
	args, _ := writeInputs(t, endpoint)

	utc, err := run(context.Background(), args)
	if err == nil || utc.HasInterval || utc.Status == gstime.StatusSynced {
		t.Fatalf("rejected upstream produced result=%+v error=%v", utc, err)
	}
}

func TestRunCancelsUnavailablePeer(t *testing.T) {
	endpoint, received := startNTPPeer(t, func(*ntp.Packet) *ntp.Packet { return nil })
	args, _ := writeInputs(t, endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, args)
		done <- err
	}()
	select {
	case <-received:
		cancel()
	case err := <-done:
		t.Fatalf("run completed before cancellation without an upstream response: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("upstream request was not received")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run returned %v", err)
	}
}

func TestRunTimesOutUnavailablePeer(t *testing.T) {
	endpoint, _ := startNTPPeer(t, func(*ntp.Packet) *ntp.Packet { return nil })
	args, _ := writeInputs(t, endpoint)
	args = append(args, "-timeout", "20ms")

	utc, err := run(context.Background(), args)
	var timeoutErr net.Error
	timedOut := errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeoutErr) && timeoutErr.Timeout()
	if !timedOut || utc.HasInterval || utc.Status == gstime.StatusSynced {
		t.Fatalf("unavailable peer produced result=%+v error=%v", utc, err)
	}
}

func TestRunRequiresBothInputFiles(t *testing.T) {
	for _, args := range [][]string{nil, {"-config", "config.json"}, {"-leaps", "history.gstl"}} {
		utc, err := run(context.Background(), args)
		if !errors.Is(err, errUsage) || utc.HasInterval || utc.Status == gstime.StatusSynced {
			t.Fatalf("args=%v returned result=%+v error=%v", args, utc, err)
		}
	}
}

func writeInputs(t *testing.T, endpoint string) ([]string, *core.LeapHistory) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Assurance.FaultBudget = 0
	cfg.Assurance.MinVotingDomains = 1
	cfg.Assurance.MinHonestCoverage = 1
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "loopback", Endpoint: endpoint}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	leapPath := filepath.Join(dir, "history.gstl")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leapPath, lh.CanonicalBytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"-config", configPath, "-leaps", leapPath}, lh
}

func startNTPPeer(t *testing.T, reply func(*ntp.Packet) *ntp.Packet) (string, <-chan struct{}) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{})
	done := make(chan error, 1)
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close peer: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("serve peer: %v", err)
		}
	})
	go func() {
		var buf [1024]byte
		n, client, err := conn.ReadFromUDP(buf[:])
		if err != nil {
			done <- err
			return
		}
		req, err := ntp.ParsePacket(buf[:n])
		if err != nil {
			done <- err
			return
		}
		close(received)
		if resp := reply(req); resp != nil {
			_, err = conn.WriteToUDP(resp.Encode(), client)
		}
		done <- err
	}()
	return conn.LocalAddr().String(), received
}
