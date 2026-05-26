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
from opentelemetry._logs import SeverityNumber, get_logger
from opentelemetry.context import Context
from opentelemetry.trace import Span, Status, StatusCode

from bargeboard import __version__ as BARGEBOARD_VERSION
from bargeboard.models import (
    DefensiveMove,
    DriverInfo,
    DriverState,
    Event,
    Flag,
    FlagColor,
    Investigation,
    InvestigationStatus,
    LapStart,
    Penalty,
    PenaltyType,
    PitEntry,
    PitExit,
    RaceControl,
    Retirement,
    SectorBoundary,
    Telemetry,
    TyreChange,
)
from bargeboard.providers import ProviderBundle, SessionBundle

# --- log severities for each event kind --------------------------------------
# Keyed for clarity. Lookup helpers live below.
_FLAG_SEVERITY = {
    FlagColor.GREEN: SeverityNumber.INFO,
    FlagColor.CHEQUERED: SeverityNumber.INFO,
    FlagColor.YELLOW: SeverityNumber.WARN,
    FlagColor.DOUBLE_YELLOW: SeverityNumber.WARN,
    FlagColor.SC: SeverityNumber.WARN,
    FlagColor.VSC: SeverityNumber.WARN,
    FlagColor.WHITE: SeverityNumber.INFO,
    FlagColor.BLACK_AND_WHITE: SeverityNumber.WARN,
    FlagColor.BLACK_AND_ORANGE: SeverityNumber.WARN,
    FlagColor.BLACK: SeverityNumber.ERROR,
    FlagColor.RED: SeverityNumber.ERROR,
    FlagColor.BLUE: SeverityNumber.DEBUG,
}

_PENALTY_SEVERITY = {
    PenaltyType.REPRIMAND: SeverityNumber.INFO,
    PenaltyType.FIVE_SECOND: SeverityNumber.WARN,
    PenaltyType.TEN_SECOND: SeverityNumber.WARN,
    PenaltyType.DRIVE_THROUGH: SeverityNumber.WARN,
    PenaltyType.STOP_GO_10: SeverityNumber.WARN,
    PenaltyType.GRID: SeverityNumber.WARN,
    PenaltyType.DSQ: SeverityNumber.ERROR,
}

_INVESTIGATION_SEVERITY = {
    InvestigationStatus.NOTED: SeverityNumber.INFO,
    InvestigationStatus.UNDER_INVESTIGATION: SeverityNumber.INFO,
    InvestigationStatus.NO_ACTION: SeverityNumber.DEBUG,
    InvestigationStatus.PENALTY: SeverityNumber.WARN,
}

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
        self.meter = bundle.meter_provider.get_meter(SCOPE_NAME, SCOPE_VERSION)
        # Session-scoped instruments: things that aren't about a single car.
        self.cars_on_track = self.meter.create_up_down_counter(
            "f1.session.cars_on_track",
            description="Number of cars still circulating (non-DNF, non-FINISHED).",
            unit="{car}",
        )
        self.root_span: Optional[Span] = None

    def car_started(self) -> None:
        self.cars_on_track.add(1)

    def car_retired(self) -> None:
        self.cars_on_track.add(-1)

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

    def __init__(
        self,
        bundle: ProviderBundle,
        wall_start: datetime,
        session_emitter: SessionEmitter,
    ):
        self.bundle = bundle
        self.driver: DriverInfo = bundle.driver
        self.wall_start = wall_start  # wall-clock at race_t = 0
        self.session_emitter = session_emitter
        self.tracer = bundle.tracer_provider.get_tracer(SCOPE_NAME, SCOPE_VERSION)
        self.meter = bundle.meter_provider.get_meter(SCOPE_NAME, SCOPE_VERSION)
        self.logger = get_logger(
            SCOPE_NAME, SCOPE_VERSION, logger_provider=bundle.logger_provider
        )

        # --- instruments --------------------------------------------------
        # Telemetry gauges (one push per Telemetry event).
        m = self.meter
        self.g_speed     = m.create_gauge("f1.car.speed",    description="Speed", unit="km/h")
        self.g_rpm       = m.create_gauge("f1.car.rpm",      description="Engine RPM", unit="{rpm}")
        self.g_throttle  = m.create_gauge("f1.car.throttle", description="Throttle position", unit="%")
        self.g_brake     = m.create_gauge("f1.car.brake",    description="Brake pressure", unit="%")
        self.g_gear      = m.create_gauge("f1.car.gear",     description="Selected gear", unit="{gear}")
        self.g_drs       = m.create_gauge("f1.car.drs",      description="DRS open (1) or closed (0)", unit="{bool}")
        self.g_pos_x     = m.create_gauge("f1.car.position_x", description="Track-local X position", unit="m")
        self.g_pos_y     = m.create_gauge("f1.car.position_y", description="Track-local Y position", unit="m")
        self.g_pos_z     = m.create_gauge("f1.car.position_z", description="Track-local Z position (elevation)", unit="m")

        # WORKSHOP: standings_position needs lap-cadence position data from FastF1.
        self.g_position  = m.create_gauge("f1.driver.standings_position",
                                          description="Current race position (1 = leader)",
                                          unit="{position}")

        # Counters — monotonic.
        self.c_laps         = m.create_counter("f1.driver.laps_completed", description="Laps completed", unit="{lap}")
        self.c_pit_stops    = m.create_counter("f1.driver.pit_stops",      description="Pit stops completed", unit="{stop}")
        self.c_blue_flags   = m.create_counter("f1.driver.blue_flags",     description="Blue flags shown to this driver", unit="{flag}")
        self.c_penalties    = m.create_counter("f1.driver.penalties",      description="Penalties received", unit="{penalty}")
        self.c_investigations = m.create_counter("f1.driver.investigations",
                                                 description="Incidents opened against this driver (counted at under_investigation)",
                                                 unit="{incident}")
        self.c_defensive_moves = m.create_counter("f1.driver.defensive_moves",
                                                  description="Lateral moves under braking detected with a chaser close behind",
                                                  unit="{move}")

        # Up-down counter — championship points can drop on DSQ.
        # WORKSHOP: needs cross-race / season state, not pushed live yet.
        self.udc_points = m.create_up_down_counter("f1.driver.championship_points",
                                                   description="Championship points (can decrease via DSQ)",
                                                   unit="{point}")

        # Explicit-bucket histograms.
        self.h_lap_time      = m.create_histogram("f1.driver.lap_time",      description="Lap time", unit="s")
        self.h_sector_time   = m.create_histogram("f1.driver.sector_time",   description="Sector time", unit="s")
        self.h_pit_duration  = m.create_histogram("f1.driver.pit_duration",  description="Pit lane duration", unit="s")

        # Exponential histograms (promoted via View on the MeterProvider).
        self.h_top_speed     = m.create_histogram("f1.driver.top_speed",     description="Top speed per lap", unit="km/h")
        # WORKSHOP: gap_to_leader needs lap-cadence timing extraction.
        self.h_gap_to_leader = m.create_histogram("f1.driver.gap_to_leader", description="Gap to race leader", unit="s")

        # --- span state ---------------------------------------------------
        self.race_span: Optional[Span] = None
        self.lap_span: Optional[Span] = None
        self.lap_number: int = 0
        self.sector_span: Optional[Span] = None
        self.sector_number: int = 0
        self.pit_span: Optional[Span] = None

        # --- timing state for histograms ----------------------------------
        self.lap_start_ts: Optional[int] = None   # ns when current lap opened
        self.sector_start_ts: Optional[int] = None
        self.pit_entry_ts: Optional[int] = None
        self.lap_top_speed: float = 0.0           # max km/h seen during current lap

        # --- per-driver state used elsewhere ------------------------------
        self.retired: bool = False

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
            self._on_pit_exit(ts_ns)
        elif isinstance(ev, TyreChange):
            self._on_tyre_change(ev, ts_ns)
        elif isinstance(ev, Telemetry):
            self._on_telemetry(ev)
        elif isinstance(ev, RaceControl):
            self._on_race_control(ev, ts_ns)
        elif isinstance(ev, Flag):
            self._on_flag(ev, ts_ns)
        elif isinstance(ev, Penalty):
            self._on_penalty(ev, ts_ns)
        elif isinstance(ev, Investigation):
            self._on_investigation(ev, ts_ns)
        elif isinstance(ev, DefensiveMove):
            self._on_defensive_move(ev, ts_ns)
        elif isinstance(ev, Retirement):
            self._on_retirement(ev, ts_ns)

    # --- span lifecycle helpers --------------------------------------------

    def _on_lap_start(self, ev: LapStart, ts_ns: int) -> None:
        # Close the previous lap and record its histograms before opening N.
        self._close_pit(ts_ns)
        self._close_sector(ts_ns)
        if self.lap_span is not None and self.lap_start_ts is not None:
            lap_seconds = (ts_ns - self.lap_start_ts) / 1e9
            self.h_lap_time.record(lap_seconds, {"f1.lap.number": self.lap_number})
            if self.lap_top_speed > 0:
                self.h_top_speed.record(self.lap_top_speed, {"f1.lap.number": self.lap_number})
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
        self.lap_start_ts = ts_ns
        self.lap_top_speed = 0.0
        self.c_laps.add(1)
        self._open_sector(1, ts_ns)

    def _on_sector_boundary(self, ev: SectorBoundary, ts_ns: int) -> None:
        # Boundary fires at the END of sector N. Record its time, close N,
        # open N+1 (if any).
        if self.sector_span is None or self.sector_number != ev.sector:
            log.debug(
                "[%s] sector boundary %d with no matching open sector (have %d)",
                self.driver.code, ev.sector, self.sector_number,
            )
        if self.sector_span is not None and self.sector_start_ts is not None:
            sector_seconds = (ts_ns - self.sector_start_ts) / 1e9
            self.h_sector_time.record(
                sector_seconds,
                {"f1.lap.number": self.lap_number, "f1.sector.number": ev.sector},
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
        self.pit_entry_ts = ts_ns

    def _on_pit_exit(self, ts_ns: int) -> None:
        # Record pit duration histogram + bump stop counter, then close span.
        if self.pit_entry_ts is not None:
            pit_seconds = (ts_ns - self.pit_entry_ts) / 1e9
            self.h_pit_duration.record(pit_seconds, {"f1.lap.number": self.lap_number})
        self.c_pit_stops.add(1)
        self._close_pit(ts_ns)

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
        self.sector_start_ts = ts_ns

    def _close_sector(self, ts_ns: int) -> None:
        if self.sector_span is not None:
            self.sector_span.end(end_time=ts_ns)
            self.sector_span = None
            self.sector_number = 0
            self.sector_start_ts = None

    def _close_lap(self, ts_ns: int) -> None:
        if self.lap_span is not None:
            self.lap_span.end(end_time=ts_ns)
            self.lap_span = None
            self.lap_start_ts = None

    def _close_pit(self, ts_ns: int) -> None:
        if self.pit_span is not None:
            self.pit_span.end(end_time=ts_ns)
            self.pit_span = None
            self.pit_entry_ts = None

    def _active_span(self) -> Optional[Span]:
        """Whichever span is the most-specific 'where the driver is right now'.
        Sector beats lap beats race. Returns None if the driver hasn't started."""
        return self.sector_span or self.lap_span or self.race_span

    # --- event handlers -----------------------------------------------------

    def _on_telemetry(self, ev: Telemetry) -> None:
        # Sync gauges — push every value. Note the timestamp wart: OTel sync
        # instruments stamp at record-time (wall-clock now), not historical.
        self.g_speed.set(ev.speed_kph)
        self.g_rpm.set(ev.rpm)
        self.g_throttle.set(ev.throttle)
        self.g_brake.set(ev.brake)
        self.g_gear.set(ev.gear)
        self.g_drs.set(1 if ev.drs else 0)
        self.g_pos_x.set(ev.x_m)
        self.g_pos_y.set(ev.y_m)
        self.g_pos_z.set(ev.z_m)
        if ev.speed_kph > self.lap_top_speed:
            self.lap_top_speed = ev.speed_kph

    def _on_tyre_change(self, ev: TyreChange, ts_ns: int) -> None:
        attrs = {"f1.tyre.compound": ev.compound, "f1.tyre.age_laps": ev.age_laps}
        self._add_event(ts_ns, "tyre_change", attrs)
        self._emit_log(
            ts_ns,
            SeverityNumber.DEBUG,
            f"{ev.compound} tyres fitted (age {ev.age_laps})",
            {"f1.tyre.compound": ev.compound, "f1.tyre.age_laps": ev.age_laps},
        )

    def _on_retirement(self, ev: Retirement, ts_ns: int) -> None:
        # Span event lands on the active span; log records the reason; the
        # session-level cars_on_track counter decrements once.
        self._add_event(ts_ns, "retirement", {"f1.retirement.reason": ev.reason})
        self._emit_log(
            ts_ns,
            SeverityNumber.ERROR,
            f"Retired: {ev.reason}",
            {"f1.retirement.reason": ev.reason},
        )
        if not self.retired:
            self.session_emitter.car_retired()
            self.retired = True

    def _on_race_control(self, ev: RaceControl, ts_ns: int) -> None:
        # Catch-all: free-form race-control messages that don't fit a typed
        # category. Span event + INFO log.
        self._add_event(ts_ns, "race_control", {"f1.race_control.message": ev.message})
        self._emit_log(ts_ns, SeverityNumber.INFO, ev.message, {})

    def _on_flag(self, ev: Flag, ts_ns: int) -> None:
        attrs = {
            "f1.flag.color": ev.color.value,
            "f1.flag.status": ev.status.value,
        }
        if ev.scope is not None:
            attrs["f1.flag.scope"] = ev.scope
        self._add_event(ts_ns, f"flag.{ev.color.value}", attrs)

        # Blue flags get counted — they're per-driver and meaningful as a tally.
        if ev.color == FlagColor.BLUE:
            self.c_blue_flags.add(1)

        severity = _FLAG_SEVERITY.get(ev.color, SeverityNumber.INFO)
        scope_str = f" ({ev.scope})" if ev.scope else ""
        self._emit_log(ts_ns, severity, f"{ev.color.value} flag {ev.status.value}{scope_str}", attrs)

    def _on_penalty(self, ev: Penalty, ts_ns: int) -> None:
        attrs = {
            "f1.penalty.type": ev.type.value,
            "f1.penalty.reason": ev.reason,
        }
        if ev.seconds is not None:
            attrs["f1.penalty.seconds"] = ev.seconds
        self._add_event(ts_ns, f"penalty.{ev.type.value}", attrs)
        self.c_penalties.add(1, {"f1.penalty.type": ev.type.value})

        severity = _PENALTY_SEVERITY.get(ev.type, SeverityNumber.WARN)
        body = f"{ev.type.value} penalty: {ev.reason}"
        self._emit_log(ts_ns, severity, body, attrs)

    def _on_defensive_move(self, ev: DefensiveMove, ts_ns: int) -> None:
        # Derived event from the pos_data / car_data pass. Lands on the
        # active sector span, bumps the counter, and logs at WARN — they're
        # genuinely interesting and probably stewards-worthy.
        attrs = {
            "f1.defensive_move.lateral_m": ev.lateral_meters,
            "f1.defensive_move.chaser_code": ev.chaser_code,
            "f1.defensive_move.chaser_gap_m": ev.chaser_gap_m,
        }
        self._add_event(ts_ns, "defensive_move", attrs)
        self.c_defensive_moves.add(1)
        self._emit_log(
            ts_ns,
            SeverityNumber.WARN,
            f"Defensive move under braking: {ev.lateral_meters:.1f}m lateral, "
            f"chaser {ev.chaser_code} {ev.chaser_gap_m:.1f}m back",
            attrs,
        )

    def _on_investigation(self, ev: Investigation, ts_ns: int) -> None:
        attrs = {
            "f1.investigation.status": ev.status.value,
            "f1.investigation.reason": ev.reason,
        }
        self._add_event(ts_ns, f"investigation.{ev.status.value}", attrs)
        # Count once per incident: at under_investigation, not at noted (too
        # early) and not at resolution (would double-count with the open).
        if ev.status == InvestigationStatus.UNDER_INVESTIGATION:
            self.c_investigations.add(1)

        severity = _INVESTIGATION_SEVERITY.get(ev.status, SeverityNumber.INFO)
        body = f"investigation {ev.status.value}: {ev.reason}"
        self._emit_log(ts_ns, severity, body, attrs)

    # --- emission helpers ---------------------------------------------------

    def _add_event(self, ts_ns: int, name: str, attrs: dict) -> None:
        """Attach a span event to the most-specific currently-open span
        (sector → lap → race). Driver code goes on every event automatically."""
        target = self._active_span()
        if target is None:
            log.debug("[%s] dropping %s — no active span", self.driver.code, name)
            return
        attrs = {**attrs, "f1.driver.code": self.driver.code}
        target.add_event(name, attributes=attrs, timestamp=ts_ns)

    def _emit_log(
        self,
        ts_ns: int,
        severity: SeverityNumber,
        body: str,
        attrs: dict,
    ) -> None:
        """Emit a log record correlated to the currently-active span. Driver
        code lives on the resource so we don't repeat it here; we DO stamp
        f1.driver.code as an attribute too so log queries don't have to
        cross-reference resource attributes."""
        attrs = {**attrs, "f1.driver.code": self.driver.code}
        target = self._active_span()
        ctx = trace.set_span_in_context(target) if target is not None else None
        self.logger.emit(
            timestamp=ts_ns,
            observed_timestamp=ts_ns,
            severity_number=severity,
            severity_text=severity.name,
            body=body,
            attributes=attrs,
            context=ctx,
        )


def make_emitters(
    bundles: Dict[str, ProviderBundle],
    wall_start: datetime,
    session_emitter: SessionEmitter,
) -> Dict[str, DriverEmitter]:
    return {
        code: DriverEmitter(b, wall_start, session_emitter)
        for code, b in bundles.items()
    }


# --- helpers ----------------------------------------------------------------

def _to_unix_nanos(when: datetime) -> int:
    """OTel SDK accepts span start/end times as Unix nanoseconds (int)."""
    return int(when.timestamp() * 1_000_000_000)
