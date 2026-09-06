package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

var (
	ErrInvalidConfig = errors.New("invalid configuration")
)

// AssuranceConfig defines deterministic assurance settings (Section 8.3).
type AssuranceConfig struct {
	FaultBudget       int   `json:"faultBudget"`
	MinVotingDomains  int   `json:"minVotingDomains"`
	MinHonestCoverage int   `json:"minHonestCoverage"`
	MaxWidthNs        int64 `json:"maxWidthNs"`
	MaxAgeNs          int64 `json:"maxAgeNs"`
	MaxHoldoverAgeNs  int64 `json:"maxHoldoverAgeNs"`
}

// RawConfig defines physical raw clock parameters.
type RawConfig struct {
	BackendProfile string  `json:"backendProfile"`
	ScaleLowerPpm  float64 `json:"scaleLowerPpm"`
	ScaleUpperPpm  float64 `json:"scaleUpperPpm"`
	ReadBoundNs    uint64  `json:"readBoundNs"`
}

// SourceConfig defines one upstream source endpoint.
type SourceConfig struct {
	FaultDomainID string `json:"faultDomainId"`
	Endpoint      string `json:"endpoint"`
	NTS           bool   `json:"nts"`
}

// IsNTS returns true if the source uses Network Time Security (NTS).
func (s SourceConfig) IsNTS() bool {
	return s.NTS
}

// Config represents the complete system configuration.
type Config struct {
	Assurance AssuranceConfig `json:"assurance"`
	Raw       RawConfig       `json:"raw"`
	Sources   []SourceConfig  `json:"sources"`
}

// DefaultConfig returns safe normative default configuration.
func DefaultConfig() Config {
	return Config{
		Assurance: AssuranceConfig{
			FaultBudget:       1,
			MinVotingDomains:  3,
			MinHonestCoverage: 2,
			MaxWidthNs:        32 * 1_000_000_000,
			MaxAgeNs:          3 * 1024 * 1_000_000_000,
			MaxHoldoverAgeNs:  86400 * 1_000_000_000,
		},
		Raw: RawConfig{
			BackendProfile: "standard_monotonic",
			ScaleLowerPpm:  -200.0,
			ScaleUpperPpm:  200.0,
			ReadBoundNs:    1000,
		},
		Sources: []SourceConfig{},
	}
}

// Validate checks all security and assurance limits.
func (c *Config) Validate() error {
	if c.Assurance.FaultBudget < 0 {
		return fmt.Errorf("%w: faultBudget cannot be negative", ErrInvalidConfig)
	}
	minVoting := 2*c.Assurance.FaultBudget + 1
	if c.Assurance.MinVotingDomains < minVoting {
		return fmt.Errorf("%w: minVotingDomains (%d) must be >= 2F+1 (%d)",
			ErrInvalidConfig, c.Assurance.MinVotingDomains, minVoting)
	}
	if c.Assurance.MinHonestCoverage < 1 {
		return fmt.Errorf("%w: minHonestCoverage must be >= 1", ErrInvalidConfig)
	}
	if c.Assurance.MaxAgeNs < 0 || c.Assurance.MaxHoldoverAgeNs < 0 {
		return fmt.Errorf("%w: age limits cannot be negative", ErrInvalidConfig)
	}
	if c.Assurance.MaxWidthNs <= 0 {
		return fmt.Errorf("%w: maxWidthNs must be positive", ErrInvalidConfig)
	}
	if c.Raw.ScaleLowerPpm > c.Raw.ScaleUpperPpm {
		return fmt.Errorf("%w: raw scale lower ppm cannot exceed upper ppm", ErrInvalidConfig)
	}
	return nil
}

// CanonicalBytes returns the RFC 8785 JSON Canonicalization Scheme (JCS) representation.
func (c *Config) CanonicalBytes() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}

	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}

	return canonicalize(generic)
}

// ConfigID calculates the SHA-256 digest of canonical configuration bytes (Section 8.2).
func (c *Config) ConfigID() ([32]byte, error) {
	canon, err := c.CanonicalBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canon), nil
}

func canonicalize(v any) ([]byte, error) {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var out []byte
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, _ := json.Marshal(k)
			out = append(out, kb...)
			out = append(out, ':')
			vb, err := canonicalize(val[k])
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		out = append(out, '}')
		return out, nil

	case []any:
		var out []byte
		out = append(out, '[')
		for i, item := range val {
			if i > 0 {
				out = append(out, ',')
			}
			b, err := canonicalize(item)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		out = append(out, ']')
		return out, nil

	default:
		return json.Marshal(val)
	}
}
