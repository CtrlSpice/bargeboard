"""OTel emission — span/metric/log writes for race events.

This module is the workshop surface: the tick loop in `replay.py` hands us a
driver's current state, its provider bundle, and the events that fell within
this tick window. Everything else (provider lifetime, queue ordering, state
transitions) is plumbing.

Right now only the race-root span is implemented — enough to verify end-to-end
delivery. Lap spans, sector spans, span events, metric points, and log records
are TODOs we'll fill in together.
"""
from __future__ import annotations

import logging
from datetime import datetime, timedelta
from typing import Dict, Iterable, Optional

from opentelemetry.trace import Span, Status, StatusCode

from bargeboard.models import (
    DriverInfo,
    DriverState,
    Event,
    LapStart,
    PitEntry,
    PitExit,
    RaceControl,
    Retirement,
    SectorBoundary,
    Telemetry,
    TyreChange,
)
from bargeboard.providers import ProviderBundle

log = logging.getLogger(__name__)


class DriverEmitter:
    """Owns the OTel emission state for one car across the replay.

    Holds open spans (the race root and, eventually, the current lap span),
    instrument handles (gauges for telemetry), and the OTel-side logger. The
    replay engine calls `start_race`, `on_event`, `end_race` and lets us decide
    how each maps onto OTLP.
    """

    def __init__(self, bundle: ProviderBundle, wall_start: datetime):
        self.bundle = bundle
        self.driver: DriverInfo = bundle.driver
        self.wall_start = wall_start  # wall-clock at race_t = 0
        self.tracer = bundle.tracer_provider.get_tracer("bargeboard.race")
        # WORKSHOP: instantiate meter + gauges here for telemetry metrics
        # WORKSHOP: instantiate logger here for race-control / pit / tyre log records
        self.race_span: Optional[Span] = None

    # --- lifecycle ----------------------------------------------------------

    def start_race(self) -> None:
        """Open the root race span at wall_start."""
        self.race_span = self.tracer.start_span(
            name=f"race {self.driver.code}",
            start_time=_to_unix_nanos(self.wall_start),
        )

    def end_race(self, final_state: DriverState, race_t: timedelta, reason: Optional[str] = None) -> None:
        """Close the root race span. DNF => error status + event with reason."""
        if self.race_span is None:
            return
        end_wall = self.wall_start + race_t
        if final_state == DriverState.DNF:
            self.race_span.add_event(
                "retirement",
                attributes={"reason": reason or "unknown"},
                timestamp=_to_unix_nanos(end_wall),
            )
            self.race_span.set_status(Status(StatusCode.ERROR, reason or "DNF"))
        else:
            self.race_span.set_status(Status(StatusCode.OK))
        self.race_span.end(end_time=_to_unix_nanos(end_wall))
        self.race_span = None

    # --- per-tick dispatch --------------------------------------------------

    def on_events(self, events: Iterable[Event]) -> None:
        """Process every event that fell into the current tick window for this driver."""
        for ev in events:
            self._dispatch(ev)

    def _dispatch(self, ev: Event) -> None:
        # WORKSHOP: this is the meat. Decide span vs span-event vs log vs metric
        # for each kind, and what attributes to attach. For now we just log to
        # stderr so the loop is observable while we wire emission up.
        if isinstance(ev, LapStart):
            log.debug("[%s] lap %d start at %s", self.driver.code, ev.lap_number, ev.race_t)
        elif isinstance(ev, SectorBoundary):
            log.debug("[%s] sector %d end at %s", self.driver.code, ev.sector, ev.race_t)
        elif isinstance(ev, PitEntry):
            log.debug("[%s] pit entry at %s", self.driver.code, ev.race_t)
        elif isinstance(ev, PitExit):
            log.debug("[%s] pit exit at %s", self.driver.code, ev.race_t)
        elif isinstance(ev, TyreChange):
            log.debug("[%s] %s tyres (age %d)", self.driver.code, ev.compound, ev.age_laps)
        elif isinstance(ev, Telemetry):
            pass  # WORKSHOP: write to gauges
        elif isinstance(ev, RaceControl):
            log.debug("[%s] race control: %s", self.driver.code, ev.message)
        elif isinstance(ev, Retirement):
            log.debug("[%s] retired: %s", self.driver.code, ev.reason)


def make_emitters(
    bundles: Dict[str, ProviderBundle],
    wall_start: datetime,
) -> Dict[str, DriverEmitter]:
    return {code: DriverEmitter(b, wall_start) for code, b in bundles.items()}


# --- helpers ----------------------------------------------------------------

def _to_unix_nanos(when: datetime) -> int:
    """OTel SDK accepts span start/end times as Unix nanoseconds (int)."""
    return int(when.timestamp() * 1_000_000_000)
