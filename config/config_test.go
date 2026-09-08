package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestCanonicalIdentityFixture(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Assurance.MaxWidthNs = 1
	cfg.Assurance.MaxAgeNs = 9007199254740993
	cfg.Assurance.MaxHoldoverAgeNs = math.MaxInt64
	cfg.Raw = RawConfig{
		BackendProfile: "R&D",
		ReadBoundNs:    math.MaxUint64,
		ScaleLowerPpm:  math.Copysign(0, -1),
		ScaleUpperPpm:  1e-7,
	}
	cfg.Sources = []SourceConfig{
		{FaultDomainID: "z", Endpoint: "z.example:4460", NTS: true},
		{FaultDomainID: "a", Endpoint: "a.example:123"},
	}
	// Independently serialized with ECMAScript JSON.stringify and SHA-256.
	const want = `{"assurance":{"faultBudget":1,"maxAgeNs":"9007199254740993","maxHoldoverAgeNs":"9223372036854775807","maxWidthNs":"1","minHonestCoverage":2,"minVotingDomains":3},"raw":{"backendProfile":"R&D","readBoundNs":"18446744073709551615","scaleLowerPpm":0,"scaleUpperPpm":1e-7},"sources":[{"endpoint":"z.example:4460","faultDomainId":"z","nts":true},{"endpoint":"a.example:123","faultDomainId":"a","nts":false}]}`
	const wantDigest = "2d5e4810456fafa7dbabc15ec0b70a982d819b9a122c1a586f5638798f96f8df"
	got, err := cfg.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("canonical bytes = %s, want %s", got, want)
	}
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(id[:]); got != wantDigest {
		t.Errorf("ConfigID = %s, want %s", got, wantDigest)
	}
}

func TestCanonicalNanosecondsPreserveIntegers(t *testing.T) {
	fields := []struct {
		name string
		set  func(*Config, uint64)
	}{
		{"maxAgeNs", func(c *Config, n uint64) { c.Assurance.MaxAgeNs = int64(n) }},
		{"maxHoldoverAgeNs", func(c *Config, n uint64) { c.Assurance.MaxHoldoverAgeNs = int64(n) }},
		{"maxWidthNs", func(c *Config, n uint64) { c.Assurance.MaxWidthNs = int64(n) }},
		{"readBoundNs", func(c *Config, n uint64) { c.Raw.ReadBoundNs = n }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			var adjacentBytes []byte
			var adjacentID [32]byte
			for _, value := range []uint64{1, 1 << 53, 1<<53 + 1, math.MaxInt64} {
				cfg := DefaultConfig()
				field.set(&cfg, value)
				canon, err := cfg.CanonicalBytes()
				if err != nil {
					t.Fatal(err)
				}
				digits := strconv.FormatUint(value, 10)
				want := `"` + field.name + `":"` + digits + `"`
				if !bytes.Contains(canon, []byte(want)) {
					t.Errorf("canonical %s=%d missing %s: %s", field.name, value, want, canon)
				}
				ordinary, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if want := `"` + field.name + `":` + digits; !bytes.Contains(ordinary, []byte(want)) {
					t.Errorf("ordinary Config JSON no longer contains numeric %s: %s", want, ordinary)
				}
				id, err := cfg.ConfigID()
				if err != nil {
					t.Fatal(err)
				}
				if value == 1<<53 {
					adjacentBytes, adjacentID = canon, id
				}
				if value == 1<<53+1 && (bytes.Equal(canon, adjacentBytes) || id == adjacentID) {
					t.Errorf("%s identities collide for 2^53 and 2^53+1", field.name)
				}
			}
		})
	}
}

func TestCanonicalNumberRFC8785(t *testing.T) {
	// RFC 8785 Appendix B IEEE-754 values and expected decimal encodings.
	cases := []struct {
		bits uint64
		want string
	}{
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			cfg := DefaultConfig()
			value := math.Float64frombits(tc.bits)
			cfg.Raw.ScaleLowerPpm, cfg.Raw.ScaleUpperPpm = value, value
			got, err := cfg.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			want := `"scaleLowerPpm":` + tc.want + `,"scaleUpperPpm":` + tc.want + `}`
			if !bytes.Contains(got, []byte(want)) {
				t.Errorf("bits %016x: canonical numbers missing %s: %s", tc.bits, want, got)
			}
		})
	}
}

func TestCanonicalStringRFC8785(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Raw.BackendProfile = "R&D<>\u2028\u2029€\x00\x0f\b\t\n\f\r\"\\/"
	got, err := cfg.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `"backendProfile":"R&D<>` + "\u2028\u2029€" + `\u0000\u000f\b\t\n\f\r\"\\/"`
	if !bytes.Contains(got, []byte(want)) {
		t.Errorf("canonical string missing %s: %s", want, got)
	}
}

func TestCanonicalRejectsInvalidUTF8(t *testing.T) {
	for _, field := range []string{"backendProfile", "faultDomainId", "endpoint"} {
		t.Run(field, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Sources = []SourceConfig{{FaultDomainID: "a", Endpoint: "a.example:123"}}
			switch field {
			case "backendProfile":
				cfg.Raw.BackendProfile = "\xff"
			case "faultDomainId":
				cfg.Sources[0].FaultDomainID = "\xff"
			case "endpoint":
				cfg.Sources[0].Endpoint = "\xff:123"
			}
			if _, err := cfg.CanonicalBytes(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("CanonicalBytes error = %v, want invalid configuration", err)
			}
		})
	}
}

func TestEndpointCannotClaimMultipleDomains(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
		nts    bool
	}{
		{"identical", "time.example:123", "time.example:123", false},
		{"hostname case and root", "TIME.Example.:123", "time.example:123", false},
		{"numeric port", "time.example:00123", "time.example:123", false},
		{"default NTP port", "time.example", "time.example:123", false},
		{"default NTS port", "time.example", "time.example:4460", true},
		{"IPv6 spelling", "[2001:0db8:0:0:0:0:0:1]:123", "[2001:db8::1]:123", false},
		{"IPv4 mapped IPv6", "[::ffff:192.0.2.1]:123", "192.0.2.1:123", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Sources = []SourceConfig{
				{FaultDomainID: "a", Endpoint: tc.first, NTS: tc.nts},
				{FaultDomainID: "b", Endpoint: tc.second, NTS: tc.nts},
				{FaultDomainID: "c", Endpoint: "other.example:123"},
			}
			if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate error = %v, want conflicting endpoint domains", err)
			}
			cfg.Sources[1].FaultDomainID = "a"
			if err := cfg.Validate(); err != nil {
				t.Fatalf("same-domain aliases rejected: %v", err)
			}
		})
	}
}

func TestDomainMayContainMultipleEndpoints(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sources = []SourceConfig{
		{FaultDomainID: "shared", Endpoint: "first.example:123"},
		{FaultDomainID: "shared", Endpoint: "second.example:123"},
		{FaultDomainID: "shared", Endpoint: "first.example:124"},
		{FaultDomainID: "independent", Endpoint: "third.example:123"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid endpoint/domain association rejected: %v", err)
	}
}

func TestConfigRejectsInvalidNumericPolicies(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
	}{
		{"negative fault budget", func(c *Config) { c.Assurance.FaultBudget = -1 }},
		{"overflowing fault budget", func(c *Config) { c.Assurance.FaultBudget = math.MaxInt }},
		{"insufficient quorum", func(c *Config) { c.Assurance.FaultBudget = 2; c.Assurance.MinVotingDomains = 4 }},
		{"nonpositive quorum", func(c *Config) { c.Assurance.MinVotingDomains = 0 }},
		{"nonpositive honest coverage", func(c *Config) { c.Assurance.MinHonestCoverage = 0 }},
		{"negative age", func(c *Config) { c.Assurance.MaxAgeNs = -1 }},
		{"negative holdover", func(c *Config) { c.Assurance.MaxHoldoverAgeNs = -1 }},
		{"nonpositive width", func(c *Config) { c.Assurance.MaxWidthNs = 0 }},
		{"NaN lower scale", func(c *Config) { c.Raw.ScaleLowerPpm = math.NaN() }},
		{"NaN upper scale", func(c *Config) { c.Raw.ScaleUpperPpm = math.NaN() }},
		{"infinite lower scale", func(c *Config) { c.Raw.ScaleLowerPpm = math.Inf(-1) }},
		{"infinite upper scale", func(c *Config) { c.Raw.ScaleUpperPpm = math.Inf(1) }},
		{"zero physical scale", func(c *Config) { c.Raw.ScaleLowerPpm = -1e6 }},
		{"negative physical scale", func(c *Config) { c.Raw.ScaleLowerPpm = -2e6 }},
		{"inverted scale", func(c *Config) { c.Raw.ScaleLowerPpm = c.Raw.ScaleUpperPpm + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.set(&cfg)
			if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate error = %v, want invalid configuration", err)
			}
		})
	}
}

func TestConfigAllowsLargestQuorumWithoutOverflow(t *testing.T) {
	maxCount := int64(math.MaxUint32)
	if strconv.IntSize == 32 {
		maxCount = math.MaxInt32
	}
	cfg := DefaultConfig()
	cfg.Assurance.MinVotingDomains = int(maxCount)
	cfg.Assurance.FaultBudget = int((maxCount - 1) / 2)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("largest representable quorum rejected: %v", err)
	}
	cfg.Assurance.FaultBudget++
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("insufficient largest quorum: error = %v, want invalid configuration", err)
	}
}
