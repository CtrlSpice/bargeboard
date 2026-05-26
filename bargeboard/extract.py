"""FastF1 → bargeboard event extraction.

Turns a loaded `fastf1.core.Session` into the typed events the replay engine
consumes. Phase 1 covers lap-derived events: lap starts, sector boundaries,
pit entry/exit, tyre changes, retirements. Telemetry (phase 2) and
race-control / flags / penalties / investigations (phase 3) land in
follow-up slices and plug into this same `extract_events` orchestrator.

All race-clock offsets come from FastF1's `SessionTime` / `LapStartTime` /
`Sector{N}SessionTime` etc., which are timedeltas from session start — the
same coordinate system as our `Event.race_t`.
"""
from __future__ import annotations

import logging
from datetime import timedelta
from typing import List

import fastf1
import pandas as pd

from bargeboard.models import (
    Event,
    LapStart,
    PitEntry,
    PitExit,
    Retirement,
    SectorBoundary,
    Telemetry,
    TyreChange,
)

log = logging.getLogger(__name__)

# session.results.Status strings that mean "finished the race" (counted, not retired).
# FastF1 has used both bare "Lapped" and "+1 Lap" / "+2 Laps" forms in different seasons.
_FINISHED_STATUSES = ("Finished", "Lapped")
_LAPPED_PREFIX = "+"   # "+1 Lap", "+2 Laps", ...
_SKIP_STATUSES = ("Did not start", "Did not qualify", "Withdrawn")

# FastF1 encodes DRS as an integer code: 0/1/8 = closed/eligible, 10/12/14 = open.
_DRS_OPEN_VALUES = (10, 12, 14)


def extract_events(session: fastf1.core.Session) -> List[Event]:
    """Pull every supported event type from `session` into one race-t-sorted list.

    Phase 1: lap-derived events + retirements. Phases 2 & 3 will append
    telemetry and race-control events to this same return value.
    """
    out: List[Event] = []
    out.extend(extract_lap_events(session))
    out.extend(extract_retirements(session))
    out.extend(extract_telemetry(session))
    out.sort(key=lambda e: e.race_t)
    log.info("extracted %d events from session", len(out))
    return out


def extract_lap_events(session: fastf1.core.Session) -> List[Event]:
    """Emit LapStart, SectorBoundary, PitEntry, PitExit, TyreChange.

    Iterates `session.laps` once per driver in lap-number order so we can
    detect compound changes between consecutive laps and pair them with the
    PitOut where the new tyres were fitted.
    """
    out: List[Event] = []
    laps = session.laps
    if laps is None or len(laps) == 0:
        log.warning("session.laps is empty — no lap-derived events")
        return out

    for driver_code, driver_laps in laps.groupby("Driver"):
        driver_laps = driver_laps.sort_values("LapNumber")
        prev_compound: str | None = None

        for _, lap in driver_laps.iterrows():
            code = str(driver_code)
            lap_number = int(lap["LapNumber"])

            lap_start = lap.get("LapStartTime")
            if pd.notna(lap_start):
                out.append(LapStart(race_t=lap_start, driver_code=code, lap_number=lap_number))

            for n in (1, 2, 3):
                t = lap.get(f"Sector{n}SessionTime")
                if pd.notna(t):
                    out.append(SectorBoundary(race_t=t, driver_code=code, sector=n))

            pit_in = lap.get("PitInTime")
            if pd.notna(pit_in):
                out.append(PitEntry(race_t=pit_in, driver_code=code))

            pit_out = lap.get("PitOutTime")
            compound = lap.get("Compound")
            tyre_life = lap.get("TyreLife")

            if pd.notna(pit_out):
                out.append(PitExit(race_t=pit_out, driver_code=code))
                # New compound after a stop → tyre change at PitOut.
                if pd.notna(compound) and compound != prev_compound:
                    out.append(TyreChange(
                        race_t=pit_out,
                        driver_code=code,
                        compound=str(compound),
                        age_laps=int(tyre_life) if pd.notna(tyre_life) else 0,
                    ))
                    prev_compound = str(compound)

            # First sighting of a compound for this driver (no preceding PitOut,
            # e.g. the starting tyres on lap 1) — record at lap start.
            if prev_compound is None and pd.notna(compound) and pd.notna(lap_start):
                out.append(TyreChange(
                    race_t=lap_start,
                    driver_code=code,
                    compound=str(compound),
                    age_laps=int(tyre_life) if pd.notna(tyre_life) else 0,
                ))
                prev_compound = str(compound)

    return out


def extract_telemetry(session: fastf1.core.Session) -> List[Telemetry]:
    """Merge car_data (RPM/Speed/Throttle/Brake/Gear/DRS) with pos_data (X/Y/Z)
    per driver and yield one Telemetry event per merged sample.

    Volume warning: ~3-4 Hz × race duration × 20 drivers can easily be
    hundreds of thousands of events. We deliberately don't downsample
    here — that's a later optimization once we see how the replay loop
    handles it.
    """
    out: List[Telemetry] = []
    car_data = getattr(session, "car_data", None)
    pos_data = getattr(session, "pos_data", None)
    if not car_data:
        log.warning("session.car_data is empty — no telemetry events")
        return out

    # session.drivers is the authoritative participant list. FastF1's
    # car_data / pos_data dicts often include phantom numbers (reserves,
    # test entries, timing-system aliases) with one or two stray samples;
    # skip anything not on the grid.
    real_drivers = set(getattr(session, "drivers", []) or [])

    for car_num, car_df in car_data.items():
        if car_df is None or len(car_df) == 0:
            continue
        if real_drivers and car_num not in real_drivers:
            log.debug("skipping non-participant car number %s", car_num)
            continue
        try:
            info = session.get_driver(car_num)
            code = str(info["Abbreviation"])
        except Exception:
            log.debug("no driver record for car %s — skipping", car_num)
            continue

        pos_df = pos_data.get(car_num) if pos_data else None
        if pos_df is not None and len(pos_df) > 0:
            merged = car_df.merge_channels(pos_df)
        else:
            merged = car_df

        # itertuples is the fastest pandas row iteration. The frame may not
        # have X/Y/Z if pos_data was missing — fall back to 0.0 per-field.
        has_xyz = all(col in merged.columns for col in ("X", "Y", "Z"))
        for row in merged.itertuples(index=False):
            t = getattr(row, "SessionTime", None)
            if t is None or pd.isna(t) or t < timedelta(0):
                # Skip pre-grid / formation-lap samples — our race-clock starts at 0.
                continue
            drs_val = int(getattr(row, "DRS", 0) or 0)
            out.append(Telemetry(
                race_t=t,
                driver_code=code,
                speed_kph=float(getattr(row, "Speed", 0) or 0),
                rpm=int(getattr(row, "RPM", 0) or 0),
                throttle=float(getattr(row, "Throttle", 0) or 0),
                brake=float(getattr(row, "Brake", 0) or 0),
                gear=int(getattr(row, "nGear", 0) or 0),
                drs=drs_val in _DRS_OPEN_VALUES,
                x_m=float(getattr(row, "X", 0) or 0) if has_xyz else 0.0,
                y_m=float(getattr(row, "Y", 0) or 0) if has_xyz else 0.0,
                z_m=float(getattr(row, "Z", 0) or 0) if has_xyz else 0.0,
            ))

    return out


def extract_retirements(session: fastf1.core.Session) -> List[Event]:
    """One Retirement event per driver who didn't finish."""
    out: List[Event] = []
    results = session.results
    if results is None or len(results) == 0:
        return out

    laps = session.laps
    for _, row in results.iterrows():
        status = str(row.get("Status", "") or "")
        if status in _FINISHED_STATUSES or status.startswith(_LAPPED_PREFIX):
            continue
        if status in _SKIP_STATUSES or not status:
            continue

        code = str(row.get("Abbreviation") or "")
        if not code:
            continue

        # Race-clock of retirement = end of their last completed lap. Falls back
        # to LapStartTime, then 0, so we always emit something useful.
        race_t = timedelta(0)
        if laps is not None:
            driver_laps = laps[laps["Driver"] == code]
            if len(driver_laps) > 0:
                last = driver_laps.sort_values("LapNumber").iloc[-1]
                lap_end = last.get("Time")
                if pd.notna(lap_end):
                    race_t = lap_end
                elif pd.notna(last.get("LapStartTime")):
                    race_t = last["LapStartTime"]

        out.append(Retirement(race_t=race_t, driver_code=code, reason=status))

    return out
