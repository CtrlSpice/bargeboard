"""FastF1 wrapper — loads a race session and extracts our domain model from it."""
from __future__ import annotations

import logging
from datetime import timedelta
from pathlib import Path
from typing import List

import fastf1

from bargeboard.models import DriverInfo, SessionInfo

log = logging.getLogger(__name__)

DEFAULT_CACHE = Path.home() / ".cache" / "bargeboard"


def configure_cache(path: Path = DEFAULT_CACHE) -> None:
    """Point FastF1 at our cache dir, creating it if needed."""
    path.mkdir(parents=True, exist_ok=True)
    fastf1.Cache.enable_cache(str(path))


def load_race(year: int, round_name: str) -> fastf1.core.Session:
    """Fetch and fully load a Race session. Round can be a country name or round number."""
    log.info("loading race: %d %s R", year, round_name)
    session = fastf1.get_session(year, round_name, "R")
    # `laps=True, telemetry=True, weather=False, messages=True` — load everything we
    # might want to emit. FastF1 caches by year+round+session, so subsequent runs
    # of the same race are fast.
    session.load(laps=True, telemetry=True, weather=False, messages=True)
    return session


def extract_session_info(session: fastf1.core.Session) -> SessionInfo:
    """Pull session-level metadata into our model."""
    # FastF1's Session has `.date` (session start) and lap data we can use to derive
    # duration. For now we use the last lap's `Time` (a timedelta from session start)
    # as a proxy for session length — refine later if a more authoritative source
    # surfaces in FastF1.
    laps = session.laps
    if laps is None or len(laps) == 0:
        duration = timedelta(0)
    else:
        duration = laps["Time"].max()

    return SessionInfo(
        year=int(session.event["EventDate"].year),
        round_name=str(session.event["EventName"]),
        session_type=str(session.name),
        start_time=session.date,
        duration=duration,
    )


def extract_drivers(session: fastf1.core.Session) -> List[DriverInfo]:
    """Build a DriverInfo for every driver that participated in the session."""
    drivers: List[DriverInfo] = []
    for code in session.drivers:
        info = session.get_driver(code)
        drivers.append(
            DriverInfo(
                code=str(info["Abbreviation"]),
                full_name=f"{info['FirstName']} {info['LastName']}",
                team=str(info["TeamName"]),
                car_number=int(info["DriverNumber"]),
            )
        )
    return drivers
