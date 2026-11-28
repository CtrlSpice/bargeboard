package f1engine

import (
	"errors"
	"time"

	"github.com/CtrlSpice/bargeboard/internal/f1id"
)

// BootstrapMetadata contains the archive routing and schedule information needed
// before playback. Replay still applies the two source records in one snapshot.
type BootstrapMetadata struct {
	Identity                     f1id.SessionIdentity
	RouteKey, CircuitKey         int64
	ScheduledStart, ScheduledEnd time.Time
}

func InspectBootstrap(info, roster []byte) (BootstrapMetadata, error) {
	descriptor, err := parseSessionInfo(info)
	if err != nil || !descriptor.identityAvailable || !descriptor.routeAvailable {
		return BootstrapMetadata{}, errors.New("invalid SessionInfo")
	}
	drivers, err := parseDriverList(roster)
	if err != nil || !coherentDriverListSnapshot(drivers) {
		return BootstrapMetadata{}, errors.New("invalid DriverList")
	}
	circuit, _ := circuitKey(info, true)
	return BootstrapMetadata{Identity: descriptor.identity.shared(), RouteKey: descriptor.routeKey, CircuitKey: circuit, ScheduledStart: descriptor.schedule.startUTC, ScheduledEnd: descriptor.schedule.endUTC}, nil
}
