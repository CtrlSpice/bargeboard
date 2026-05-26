"""Core data model — sessions, drivers, per-driver state, and replay events.

Events are the unit of replay: each is timestamped with a `race_t` offset from
session start and tagged with the driver it belongs to. The replay engine
consumes a pre-sorted queue of events per driver.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import Enum
from typing import Optional, Union


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
    """Catch-all for race-control messages that don't fit a typed category
    (warnings, track-limits notes, generic info). Typed events below cover
    flags, penalties, and investigations."""
    race_t: timedelta
    driver_code: str   # may be a sentinel like "*" for session-wide messages
    message: str


class FlagColor(str, Enum):
    YELLOW = "yellow"
    DOUBLE_YELLOW = "double_yellow"
    GREEN = "green"
    RED = "red"
    BLUE = "blue"
    WHITE = "white"
    CHEQUERED = "chequered"
    BLACK = "black"
    BLACK_AND_WHITE = "black_and_white"   # warning, unsporting behaviour
    BLACK_AND_ORANGE = "black_and_orange"  # mechanical, return to pits
    SC = "sc"     # safety car
    VSC = "vsc"   # virtual safety car


class FlagStatus(str, Enum):
    DEPLOYED = "deployed"
    WITHDRAWN = "withdrawn"
    ENDING = "ending"   # VSC ending phase, SC in-this-lap, etc.


@dataclass(frozen=True)
class Flag:
    race_t: timedelta
    driver_code: str               # "*" for session-wide (most flags)
    color: FlagColor
    status: FlagStatus
    scope: Optional[str] = None    # "track" | "sector_1" | "sector_2" | "sector_3"


class PenaltyType(str, Enum):
    FIVE_SECOND = "5_second"
    TEN_SECOND = "10_second"
    DRIVE_THROUGH = "drive_through"
    STOP_GO_10 = "stop_go_10"
    GRID = "grid"
    REPRIMAND = "reprimand"
    DSQ = "disqualification"


@dataclass(frozen=True)
class Penalty:
    race_t: timedelta
    driver_code: str               # always a specific driver
    type: PenaltyType
    reason: str
    seconds: Optional[float] = None  # for time penalties


class InvestigationStatus(str, Enum):
    NOTED = "noted"
    UNDER_INVESTIGATION = "under_investigation"
    NO_ACTION = "no_action"
    PENALTY = "penalty"   # resolved with a penalty (see Penalty event for details)


@dataclass(frozen=True)
class Investigation:
    race_t: timedelta
    driver_code: str
    status: InvestigationStatus
    reason: str


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
    Flag,
    Penalty,
    Investigation,
    Retirement,
]
