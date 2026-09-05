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
