//go:build linux

package clock

import (
	"syscall"
	"time"
	"unsafe"

	"gosuda.org/gstime/core"
)

const clockBoottime = 7 // CLOCK_BOOTTIME on Linux (Section 5.1)

// LinuxBootRawClock implements RawClock using Linux CLOCK_BOOTTIME,
// advancing across sleep and suspend (Section 5.1).
type LinuxBootRawClock struct {
	startNanos int64
	startRaw   core.RawNanos
}

// NewLinuxBootRawClock creates a CLOCK_BOOTTIME raw clock backend.
func NewLinuxBootRawClock() (*LinuxBootRawClock, error) {
	now, err := boottimeNanos()
	if err != nil {
		return nil, err
	}
	return &LinuxBootRawClock{
		startNanos: now,
		startRaw:   1_000_000_000,
	}, nil
}

func boottimeNanos() (int64, error) {
	var ts syscall.Timespec
	_, _, err := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, uintptr(clockBoottime), uintptr(unsafe.Pointer(&ts)), 0)
	if err != 0 {
		return 0, err
	}
	return ts.Sec*1_000_000_000 + ts.Nsec, nil
}

func (c *LinuxBootRawClock) Read() RawReading {
	now, err := boottimeNanos()
	if err != nil {
		now = time.Now().UnixNano()
	}
	elapsed := now - c.startNanos
	if elapsed < 0 {
		elapsed = 0
	}
	return RawReading{
		Raw:               c.startRaw + core.RawNanos(elapsed),
		ReadBound:         1000,
		ContinuityToken:   1,
		BackendGeneration: 1,
	}
}

func (c *LinuxBootRawClock) ScaleEnvelope() (core.RateScale, core.RateScale) {
	low := core.RateScale(core.OneQ48 + core.RateFromPpmLower(-200.0))
	upp := core.RateScale(core.OneQ48 + core.RateFromPpmUpper(200.0))
	return low, upp
}

func (c *LinuxBootRawClock) IncludesSuspend() bool {
	return true
}
