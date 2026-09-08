package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"
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
	a := c.Assurance
	if a.FaultBudget < 0 || uint64(a.FaultBudget) > math.MaxUint32 {
		return fmt.Errorf("%w: faultBudget must fit uint32", ErrInvalidConfig)
	}
	if a.MinVotingDomains < 1 || uint64(a.MinVotingDomains) > math.MaxUint32 {
		return fmt.Errorf("%w: minVotingDomains must be positive and fit uint32", ErrInvalidConfig)
	}
	if a.FaultBudget > (a.MinVotingDomains-1)/2 {
		return fmt.Errorf("%w: minVotingDomains (%d) must be >= 2F+1 for faultBudget %d",
			ErrInvalidConfig, a.MinVotingDomains, a.FaultBudget)
	}
	if a.MinHonestCoverage < 1 || uint64(a.MinHonestCoverage) > math.MaxUint32 {
		return fmt.Errorf("%w: minHonestCoverage must be positive and fit uint32", ErrInvalidConfig)
	}
	if a.MaxAgeNs <= 0 || a.MaxHoldoverAgeNs < 0 {
		return fmt.Errorf("%w: maxAgeNs must be positive and maxHoldoverAgeNs non-negative", ErrInvalidConfig)
	}
	if a.MaxWidthNs <= 0 {
		return fmt.Errorf("%w: maxWidthNs must be positive", ErrInvalidConfig)
	}
	if math.IsNaN(c.Raw.ScaleLowerPpm) || math.IsInf(c.Raw.ScaleLowerPpm, 0) ||
		math.IsNaN(c.Raw.ScaleUpperPpm) || math.IsInf(c.Raw.ScaleUpperPpm, 0) {
		return fmt.Errorf("%w: raw scale bounds must be finite", ErrInvalidConfig)
	}
	if c.Raw.ScaleLowerPpm <= -1e6 || c.Raw.ScaleLowerPpm > c.Raw.ScaleUpperPpm {
		return fmt.Errorf("%w: raw scales must be positive and ordered", ErrInvalidConfig)
	}
	if !utf8.ValidString(c.Raw.BackendProfile) {
		return fmt.Errorf("%w: backendProfile is not valid UTF-8", ErrInvalidConfig)
	}
	domains := make(map[string]string, len(c.Sources))
	for _, source := range c.Sources {
		if source.FaultDomainID == "" || !utf8.ValidString(source.FaultDomainID) {
			return fmt.Errorf("%w: faultDomainId must be nonempty valid UTF-8", ErrInvalidConfig)
		}
		endpoint, err := normalizedEndpoint(source)
		if err != nil {
			return err
		}
		if domain, ok := domains[endpoint]; ok && domain != source.FaultDomainID {
			return fmt.Errorf("%w: endpoint %q belongs to both %q and %q",
				ErrInvalidConfig, endpoint, domain, source.FaultDomainID)
		}
		domains[endpoint] = source.FaultDomainID
	}
	return nil
}

// CanonicalBytes returns the RFC 8785 identity representation. Nanosecond fields
// are decimal strings at every magnitude; ordinary Config JSON remains numeric.
func (c *Config) CanonicalBytes() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	a := c.Assurance
	// The fixed ASCII property names are already in UTF-16 sort order.
	out := append([]byte(nil), `{"assurance":{"faultBudget":`...)
	out = strconv.AppendInt(out, int64(a.FaultBudget), 10)
	out = append(out, `,"maxAgeNs":"`...)
	out = strconv.AppendInt(out, a.MaxAgeNs, 10)
	out = append(out, `","maxHoldoverAgeNs":"`...)
	out = strconv.AppendInt(out, a.MaxHoldoverAgeNs, 10)
	out = append(out, `","maxWidthNs":"`...)
	out = strconv.AppendInt(out, a.MaxWidthNs, 10)
	out = append(out, `","minHonestCoverage":`...)
	out = strconv.AppendInt(out, int64(a.MinHonestCoverage), 10)
	out = append(out, `,"minVotingDomains":`...)
	out = strconv.AppendInt(out, int64(a.MinVotingDomains), 10)
	out = append(out, `},"raw":{"backendProfile":`...)
	out = appendCanonicalString(out, c.Raw.BackendProfile)
	out = append(out, `,"readBoundNs":"`...)
	out = strconv.AppendUint(out, c.Raw.ReadBoundNs, 10)
	out = append(out, `","scaleLowerPpm":`...)
	out = appendCanonicalNumber(out, c.Raw.ScaleLowerPpm)
	out = append(out, `,"scaleUpperPpm":`...)
	out = appendCanonicalNumber(out, c.Raw.ScaleUpperPpm)
	out = append(out, `},"sources":`...)
	if c.Sources == nil {
		return append(out, `null}`...), nil
	}
	out = append(out, '[')
	for i, source := range c.Sources {
		if i != 0 {
			out = append(out, ',')
		}
		out = append(out, `{"endpoint":`...)
		out = appendCanonicalString(out, source.Endpoint)
		out = append(out, `,"faultDomainId":`...)
		out = appendCanonicalString(out, source.FaultDomainID)
		out = append(out, `,"nts":`...)
		out = strconv.AppendBool(out, source.NTS)
		out = append(out, '}')
	}
	return append(out, ']', '}'), nil
}

// ConfigID calculates the SHA-256 digest of canonical configuration bytes (Section 8.2).
func (c *Config) ConfigID() ([32]byte, error) {
	canon, err := c.CanonicalBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canon), nil
}

func normalizedEndpoint(source SourceConfig) (string, error) {
	if !utf8.ValidString(source.Endpoint) {
		return "", fmt.Errorf("%w: endpoint is not valid UTF-8", ErrInvalidConfig)
	}
	host, port, err := net.SplitHostPort(source.Endpoint)
	if err != nil {
		host = source.Endpoint
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		if _, err := netip.ParseAddr(host); err != nil && strings.Contains(host, ":") {
			return "", fmt.Errorf("%w: malformed endpoint %q", ErrInvalidConfig, source.Endpoint)
		}
		port = "123"
		if source.NTS {
			port = "4460"
		}
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\[]") {
		return "", fmt.Errorf("%w: invalid endpoint host %q", ErrInvalidConfig, source.Endpoint)
	}
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.Unmap().String()
	} else {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if host == "" {
			return "", fmt.Errorf("%w: empty endpoint hostname", ErrInvalidConfig)
		}
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", fmt.Errorf("%w: invalid endpoint port %q", ErrInvalidConfig, port)
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}

// Input strings are validated as UTF-8 before encoding; JCS leaves non-ASCII
// bytes, including U+2028 and U+2029, unchanged rather than HTML-escaping them.
func appendCanonicalString(out []byte, value string) []byte {
	const hex = "0123456789abcdef"
	out = append(out, '"')
	for i := range len(value) {
		b := value[i]
		switch b {
		case '"', '\\':
			out = append(out, '\\', b)
		case '\b':
			out = append(out, `\b`...)
		case '\t':
			out = append(out, `\t`...)
		case '\n':
			out = append(out, `\n`...)
		case '\f':
			out = append(out, `\f`...)
		case '\r':
			out = append(out, `\r`...)
		default:
			if b < 0x20 {
				out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
			} else {
				out = append(out, b)
			}
		}
	}
	return append(out, '"')
}

func appendCanonicalNumber(out []byte, value float64) []byte {
	if value == 0 {
		return append(out, '0')
	}
	abs := math.Abs(value)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.AppendFloat(out, value, 'f', -1, 64)
	}
	out = strconv.AppendFloat(out, value, 'e', -1, 64)
	// ECMAScript omits the leading zero in a single-digit exponent.
	n := len(out)
	if out[n-4] == 'e' && out[n-2] == '0' {
		out[n-2] = out[n-1]
		out = out[:n-1]
	}
	return out
}
