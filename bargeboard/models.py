"""Core data model — sessions, drivers, per-driver state, and replay events.

Events are the unit of replay: each is timestamped with a `race_t` offset from
session start and tagged with the driver it belongs to. The replay engine
consumes a pre-sorted queue of events per driver.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import Enum
from typing import Union


class DriverState(str, Enum):
    PRE_RACE = "pre_race"
    RACING = "racing"
    PIT = "pit"
    VSC = "vsc"
    SC = "sc"
    DNF = "dnf"
    FINISHED = "finished"


@dataclass(frozen=True)
class SessionInfo:
    year: int
    round_name: str        # e.g. "Canada"
    session_type: str      # e.g. "Race"
    start_time: datetime   # absolute wall-clock of session start (from FastF1)
    duration: timedelta    # session-end minus session-start


@dataclass(frozen=True)
class DriverInfo:
    code: str          # "ANT"
    full_name: str     # "Kimi Antonelli"
    team: str          # "Mercedes"
    car_number: int    # 12


# --- Replay events -----------------------------------------------------------
# Each event carries its race-clock offset from session start plus the driver
# it belongs to. The replay engine sorts these per driver and dispatches them
# to the OTel emitter as each tick window passes their `race_t`.

@dataclass(frozen=True)
class LapStart:
    race_t: timedelta
    driver_code: str
    lap_number: int


@dataclass(frozen=True)
class SectorBoundary:
    race_t: timedelta
    driver_code: str
    sector: int  # 1, 2, or 3 — fires at the END of that sector


@dataclass(frozen=True)
class PitEntry:
    race_t: timedelta
    driver_code: str


@dataclass(frozen=True)
class PitExit:
    race_t: timedelta
    driver_code: str


@dataclass(frozen=True)
class TyreChange:
    race_t: timedelta
    driver_code: str
    compound: str   # "SOFT" / "MEDIUM" / "HARD" / "INTERMEDIATE" / "WET"
    age_laps: int


@dataclass(frozen=True)
class Telemetry:
    race_t: timedelta
    driver_code: str
    speed_kph: float
    rpm: int
    throttle: float   # 0–100
    brake: float      # 0–100
    gear: int         # 0–8
    drs: bool


@dataclass(frozen=True)
class RaceControl:
    race_t: timedelta
    driver_code: str   # may be a sentinel like "*" for session-wide messages
    message: str


@dataclass(frozen=True)
class Retirement:
    race_t: timedelta
    driver_code: str
    reason: str        # e.g. "power unit failure"


Event = Union[
    LapStart,
    SectorBoundary,
    PitEntry,
    PitExit,
    TyreChange,
    Telemetry,
    RaceControl,
    Retirement,
]
