package config

import (
	"bytes"
	"testing"
)

func TestCanonicalJCSDeterministic(t *testing.T) {
	cfg1 := DefaultConfig()
	cfg2 := DefaultConfig()

	b1, err := cfg1.CanonicalBytes()
	if err != nil {
		t.Fatalf("cfg1 CanonicalBytes: %v", err)
	}
	b2, err := cfg2.CanonicalBytes()
	if err != nil {
		t.Fatalf("cfg2 CanonicalBytes: %v", err)
	}

	if !bytes.Equal(b1, b2) {
		t.Fatalf("canonical bytes not identical across identical configs")
	}

	id1, _ := cfg1.ConfigID()
	id2, _ := cfg2.ConfigID()
	if id1 != id2 {
		t.Fatalf("ConfigID not identical: %x vs %x", id1, id2)
	}
}

func TestConfigValidation(t *testing.T) {
	// FaultBudget = 2 requires minVotingDomains >= 2*2+1 = 5
	cfg := DefaultConfig()
	cfg.Assurance.FaultBudget = 2
	cfg.Assurance.MinVotingDomains = 4 // invalid!

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for MinVotingDomains < 2F+1")
	}
}

func TestSourceConfigNTS(t *testing.T) {
	s1 := SourceConfig{
		FaultDomainID: "cloudflare",
		Endpoint:      "time.cloudflare.com:4460",
		NTS:           true,
	}
	s2 := SourceConfig{
		FaultDomainID: "google",
		Endpoint:      "time.google.com:123",
		NTS:           false,
	}

	if !s1.IsNTS() {
		t.Fatalf("expected s1.IsNTS() to be true")
	}
	if s2.IsNTS() {
		t.Fatalf("expected s2.IsNTS() to be false")
	}

	cfg1 := DefaultConfig()
	cfg1.Sources = []SourceConfig{s1, s2}

	b, err := cfg1.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes failed: %v", err)
	}

	if !bytes.Contains(b, []byte(`"nts":true`)) {
		t.Fatalf("canonical JSON missing expected \"nts\":true, got: %s", string(b))
	}
	if !bytes.Contains(b, []byte(`"nts":false`)) {
		t.Fatalf("canonical JSON missing expected \"nts\":false, got: %s", string(b))
	}
}
