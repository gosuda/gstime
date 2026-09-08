package gstime

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"time"

	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

func newNTPRequest() (ntp.Packet, error) {
	request := ntp.Packet{Version: 4, Mode: 3, Poll: 6}
	for request.TxTimestamp == [8]byte{} {
		if _, err := rand.Read(request.TxTimestamp[:]); err != nil {
			return ntp.Packet{}, err
		}
	}
	return request, nil
}

func (q *defaultSourceQuerier) captureStamp() (ntp.Stamp, error) {
	reading := q.rawClock.Read()
	if reading.ContinuityToken == 0 {
		return ntp.Stamp{}, errRawChanged
	}
	lower, upper := q.rawClock.ScaleEnvelope()
	var publication core.Generation
	bootstrap := false
	if q.service != nil {
		snapshot := q.service.publisher.Acquire()
		publication = snapshot.Generation
		bootstrap = rawDomainChanged(snapshot, reading.ContinuityToken)
	}
	var unix int64
	var mapping clock.EstimateMapping
	if bootstrap {
		mapping = q.estClock.Snapshot()
		mapping.Anchored = false
		unix = time.Now().UnixNano()
	} else {
		var err error
		unix, mapping, err = q.localUnixEstimate(reading.Raw)
		if err != nil {
			return ntp.Stamp{}, err
		}
	}
	estimate, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(unix))
	if err != nil {
		return ntp.Stamp{}, err
	}
	return ntp.Stamp{
		Raw: reading.Raw, Estimate: estimate, EstimateReadError: 1,
		MappingGeneration: mapping.MappingGeneration, StepGeneration: mapping.StepGeneration,
		PublicationGeneration: publication,
		EstimateAnchored:      mapping.Anchored,
		ContinuityToken:       reading.ContinuityToken, BackendGeneration: reading.BackendGeneration,
		RawReadBound: reading.ReadBound, RawScaleLower: lower, RawScaleUpper: upper,
	}, nil
}

func setQueryDeadline(ctx context.Context, connection net.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	return connection.SetDeadline(deadline)
}

func closeQuery(ctx context.Context, connection net.Conn, queryErr error) error {
	closeErr := connection.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(queryErr, ctx.Err(), closeErr)
}
