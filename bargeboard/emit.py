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

from opentelemetry import trace
from opentelemetry._logs import get_logger
from opentelemetry.context import Context
from opentelemetry.trace import Span, Status, StatusCode

from bargeboard import __version__ as BARGEBOARD_VERSION
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
from bargeboard.providers import ProviderBundle, SessionBundle

log = logging.getLogger(__name__)

# Instrumentation scope — identifies bargeboard as the emitting library across
# tracer, meter, and logger. One scope, one name, one version. Resource carries
# the *what* (which car); scope carries the *who* (which emitter).
SCOPE_NAME = "bargeboard"
SCOPE_VERSION = BARGEBOARD_VERSION


class SessionEmitter:
    """Owns the race-wide root span every driver's spans hang under.

    One trace per race: this emitter opens the root, hands out its context to
    each `DriverEmitter` so their per-driver `*_race` spans become children,
    then closes the root once everyone is done.
    """

    def __init__(self, bundle: SessionBundle, wall_start: datetime):
        self.bundle = bundle
        self.session = bundle.session
        self.wall_start = wall_start
        self.tracer = bundle.tracer_provider.get_tracer(SCOPE_NAME, SCOPE_VERSION)
        self.root_span: Optional[Span] = None

    def start_session(self) -> None:
        name = (
            f"{self.session.year}_"
            f"{self.session.round_name.lower().replace(' ', '_')}_"
            f"{self.session.session_type.lower()}"
        )
        self.root_span = self.tracer.start_span(
            name=name,
            start_time=_to_unix_nanos(self.wall_start),
        )

    def end_session(self, race_t: timedelta) -> None:
        if self.root_span is None:
            return
        end_ns = _to_unix_nanos(self.wall_start + race_t)
        self.root_span.set_status(Status(StatusCode.OK))
        self.root_span.end(end_time=end_ns)
        self.root_span = None

    def parent_context(self) -> Optional[Context]:
        """Context that makes the session root span the implicit parent."""
        if self.root_span is None:
            return None
        return trace.set_span_in_context(self.root_span)


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
        self.tracer = bundle.tracer_provider.get_tracer(SCOPE_NAME, SCOPE_VERSION)
        self.meter = bundle.meter_provider.get_meter(SCOPE_NAME, SCOPE_VERSION)
        self.logger = get_logger(
            SCOPE_NAME, SCOPE_VERSION, logger_provider=bundle.logger_provider
        )
        # WORKSHOP: gauge instruments hang off self.meter (telemetry)
        # WORKSHOP: log records emit via self.logger (race-control / pit / tyre)
        self.race_span: Optional[Span] = None
        self.lap_span: Optional[Span] = None
        self.lap_number: int = 0
        self.sector_span: Optional[Span] = None
        self.sector_number: int = 0
        self.pit_span: Optional[Span] = None

    # --- lifecycle ----------------------------------------------------------

    def start_race(self, parent_context: Optional[Context] = None) -> None:
        """Open the per-driver race span. If `parent_context` is given (from
        the SessionEmitter), this span becomes a child of the race root and
        joins the same trace as the other drivers."""
        self.race_span = self.tracer.start_span(
            name=f"{self.driver.code}_race",
            context=parent_context,
            start_time=_to_unix_nanos(self.wall_start),
        )

    def end_race(self, final_state: DriverState, race_t: timedelta, reason: Optional[str] = None) -> None:
        """Close the root race span. DNF => error status + event with reason."""
        if self.race_span is None:
            return
        end_wall = self.wall_start + race_t
        end_ns = _to_unix_nanos(end_wall)
        # Close any still-open children so nothing leaks past the race.
        self._close_pit(end_ns)
        self._close_sector(end_ns)
        self._close_lap(end_ns)
        if final_state == DriverState.DNF:
            self.race_span.add_event(
                "retirement",
                attributes={"reason": reason or "unknown"},
                timestamp=end_ns,
            )
            self.race_span.set_status(Status(StatusCode.ERROR, reason or "DNF"))
        else:
            self.race_span.set_status(Status(StatusCode.OK))
        self.race_span.end(end_time=end_ns)
        self.race_span = None

    # --- per-tick dispatch --------------------------------------------------

    def on_events(self, events: Iterable[Event]) -> None:
        """Process every event that fell into the current tick window for this driver."""
        for ev in events:
            self._dispatch(ev)

    def _dispatch(self, ev: Event) -> None:
        ts_ns = _to_unix_nanos(self.wall_start + ev.race_t)
        if isinstance(ev, LapStart):
            self._on_lap_start(ev, ts_ns)
        elif isinstance(ev, SectorBoundary):
            self._on_sector_boundary(ev, ts_ns)
        elif isinstance(ev, PitEntry):
            self._on_pit_entry(ts_ns)
        elif isinstance(ev, PitExit):
            self._close_pit(ts_ns)
        elif isinstance(ev, TyreChange):
            # WORKSHOP: attach as span event on the active pit span (or lap if no pit)
            log.debug("[%s] %s tyres (age %d)", self.driver.code, ev.compound, ev.age_laps)
        elif isinstance(ev, Telemetry):
            pass  # WORKSHOP: write to gauges
        elif isinstance(ev, RaceControl):
            # WORKSHOP: emit as log record + add span event on race root
            log.debug("[%s] race control: %s", self.driver.code, ev.message)
        elif isinstance(ev, Retirement):
            log.debug("[%s] retired: %s", self.driver.code, ev.reason)

    # --- span lifecycle helpers --------------------------------------------

    def _on_lap_start(self, ev: LapStart, ts_ns: int) -> None:
        # New lap closes any leftover sector/pit from the previous lap, then
        # the previous lap span itself, then opens lap N + sector 1.
        self._close_pit(ts_ns)
        self._close_sector(ts_ns)
        self._close_lap(ts_ns)
        if self.race_span is None:
            return
        race_ctx = trace.set_span_in_context(self.race_span)
        self.lap_span = self.tracer.start_span(
            name=f"{self.driver.code}_L{ev.lap_number}",
            context=race_ctx,
            start_time=ts_ns,
            attributes={"f1.lap.number": ev.lap_number},
        )
        self.lap_number = ev.lap_number
        self._open_sector(1, ts_ns)

    def _on_sector_boundary(self, ev: SectorBoundary, ts_ns: int) -> None:
        # Boundary fires at the END of sector N. Close N, open N+1 (if any).
        if self.sector_span is None or self.sector_number != ev.sector:
            log.debug(
                "[%s] sector boundary %d with no matching open sector (have %d)",
                self.driver.code, ev.sector, self.sector_number,
            )
        self._close_sector(ts_ns)
        if ev.sector < 3 and self.lap_span is not None:
            self._open_sector(ev.sector + 1, ts_ns)

    def _on_pit_entry(self, ts_ns: int) -> None:
        if self.lap_span is None:
            return
        lap_ctx = trace.set_span_in_context(self.lap_span)
        self.pit_span = self.tracer.start_span(
            name=f"{self.driver.code}_L{self.lap_number}_pit",
            context=lap_ctx,
            start_time=ts_ns,
            attributes={"f1.lap.number": self.lap_number},
        )

    def _open_sector(self, n: int, ts_ns: int) -> None:
        if self.lap_span is None:
            return
        lap_ctx = trace.set_span_in_context(self.lap_span)
        self.sector_span = self.tracer.start_span(
            name=f"{self.driver.code}_L{self.lap_number}_S{n}",
            context=lap_ctx,
            start_time=ts_ns,
            attributes={"f1.lap.number": self.lap_number, "f1.sector.number": n},
        )
        self.sector_number = n

    def _close_sector(self, ts_ns: int) -> None:
        if self.sector_span is not None:
            self.sector_span.end(end_time=ts_ns)
            self.sector_span = None
            self.sector_number = 0

    def _close_lap(self, ts_ns: int) -> None:
        if self.lap_span is not None:
            self.lap_span.end(end_time=ts_ns)
            self.lap_span = None

    def _close_pit(self, ts_ns: int) -> None:
        if self.pit_span is not None:
            self.pit_span.end(end_time=ts_ns)
            self.pit_span = None


def make_emitters(
    bundles: Dict[str, ProviderBundle],
    wall_start: datetime,
) -> Dict[str, DriverEmitter]:
    return {code: DriverEmitter(b, wall_start) for code, b in bundles.items()}


# --- helpers ----------------------------------------------------------------

def _to_unix_nanos(when: datetime) -> int:
    """OTel SDK accepts span start/end times as Unix nanoseconds (int)."""
    return int(when.timestamp() * 1_000_000_000)
