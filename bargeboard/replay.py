"""Tick-driven replay engine.

The clock advances in fixed 100ms wall-clock ticks; each tick advances the race
clock by `100ms × speed`. Events whose `race_t` falls inside the new race-clock
window are dispatched to the per-driver emitter. There is no per-event sleep —
pacing is uniform tick-to-tick, so pause/resume and speed changes are trivial.

Event queues are pre-sorted per driver and consumed from the front. Today they
arrive empty (the FastF1 → events conversion is a workshop item); the loop
still runs end-to-end, opens & closes a race root span per driver, and flushes.
"""
from __future__ import annotations

import logging
import time
from dataclasses import dataclass, field, replace
from datetime import datetime, timedelta
from typing import Dict, Iterable, List

from bargeboard.emit import DriverEmitter, SessionEmitter
from bargeboard.models import DriverInfo, DriverState, Event, SessionInfo

SESSION_WIDE = "*"

log = logging.getLogger(__name__)

TICK = timedelta(milliseconds=100)


@dataclass
class DriverRuntime:
    info: DriverInfo
    state: DriverState = DriverState.PRE_RACE
    queue: List[Event] = field(default_factory=list)
    queue_pos: int = 0  # index into `queue` of the next un-dispatched event

    def drain_until(self, race_t: timedelta) -> List[Event]:
        """Pop every event with `event.race_t <= race_t`."""
        out: List[Event] = []
        while self.queue_pos < len(self.queue) and self.queue[self.queue_pos].race_t <= race_t:
            out.append(self.queue[self.queue_pos])
            self.queue_pos += 1
        return out


@dataclass
class ReplayConfig:
    speed: float = 1.0          # race-clock multiplier (paced mode only)
    dump: bool = False          # if True, no wall-clock pacing — fire as fast as possible
    start_at: timedelta = timedelta(0)
    end_at: timedelta | None = None  # None = session.duration


class ReplayEngine:
    def __init__(
        self,
        session: SessionInfo,
        session_emitter: SessionEmitter,
        runtimes: Dict[str, DriverRuntime],
        emitters: Dict[str, DriverEmitter],
        config: ReplayConfig,
    ):
        self.session = session
        self.session_emitter = session_emitter
        self.runtimes = runtimes
        self.emitters = emitters
        self.config = config
        self.end_at = config.end_at if config.end_at is not None else session.duration

    def run(self) -> None:
        # Open the race-wide root span first, then per-driver spans as children
        # so the whole race lives in one trace.
        self.session_emitter.start_session()
        parent_ctx = self.session_emitter.parent_context()
        for code, em in self.emitters.items():
            em.start_race(parent_ctx)
            self.runtimes[code].state = DriverState.RACING
            self.session_emitter.car_started()

        race_t = self.config.start_at
        wall_next = time.monotonic()

        try:
            while race_t < self.end_at:
                race_t = min(race_t + TICK * self.config.speed, self.end_at)
                self._tick(race_t)

                if not self.config.dump:
                    wall_next += TICK.total_seconds()
                    sleep_for = wall_next - time.monotonic()
                    if sleep_for > 0:
                        time.sleep(sleep_for)
                    else:
                        # We're behind — skip the sleep but don't accumulate drift.
                        wall_next = time.monotonic()
        finally:
            # Close every still-open driver span at whatever race_t we reached,
            # then close the race-wide root.
            for code, em in self.emitters.items():
                rt = self.runtimes[code]
                final_state = (
                    DriverState.FINISHED
                    if rt.state not in (DriverState.DNF, DriverState.FINISHED)
                    else rt.state
                )
                em.end_race(final_state, race_t)
            self.session_emitter.end_session(race_t)

    def _tick(self, race_t: timedelta) -> None:
        for code, rt in self.runtimes.items():
            if rt.state == DriverState.DNF or rt.state == DriverState.FINISHED:
                continue
            events = rt.drain_until(race_t)
            if events:
                self.emitters[code].on_events(events)
                # WORKSHOP: state transitions driven by event kind go here —
                # PitEntry -> PIT, PitExit -> RACING, Retirement -> DNF, etc.


def build_runtimes(drivers: List[DriverInfo]) -> Dict[str, DriverRuntime]:
    """One DriverRuntime per driver, with empty event queues."""
    # WORKSHOP: populate `queue` from FastF1 lap/telemetry/race-control data.
    # When events are produced, route them through `fan_out_events` so
    # session-wide messages (driver_code == "*") land on every driver.
    return {d.code: DriverRuntime(info=d) for d in drivers}


def fan_out_events(
    events: Iterable[Event],
    driver_codes: List[str],
) -> Dict[str, List[Event]]:
    """Route a mixed stream of events into per-driver queues.

    Events with a real driver code go to that one driver. Events with the
    session-wide sentinel ("*") are duplicated to every driver's queue
    with the sentinel replaced by the driver's actual code, so each one
    lands as a span event on that driver's currently-active span.

    Resulting per-driver lists are sorted by race_t so the engine can
    consume them in order.
    """
    out: Dict[str, List[Event]] = {code: [] for code in driver_codes}
    for ev in events:
        code = getattr(ev, "driver_code", None)
        if code == SESSION_WIDE:
            for d in driver_codes:
                out[d].append(replace(ev, driver_code=d))
        elif code in out:
            out[code].append(ev)
        # events for unknown codes (drivers we filtered out) are silently dropped
    for d in out:
        out[d].sort(key=lambda e: e.race_t)
    return out
