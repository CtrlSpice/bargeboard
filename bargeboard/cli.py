"""Typer CLI for bargeboard."""
from __future__ import annotations

import logging
import re
from datetime import datetime, timedelta
from typing import List, Optional, Tuple

import typer

from bargeboard import session as session_mod
from bargeboard.emit import make_emitters
from bargeboard.providers import make_provider_bundles
from bargeboard.replay import ReplayConfig, ReplayEngine, build_runtimes

app = typer.Typer(add_completion=False, help="Replay F1 race sessions as OpenTelemetry signals.")


def _parse_session(value: str) -> Tuple[int, str]:
    """`2026-canada` -> (2026, "canada")."""
    m = re.match(r"^(\d{4})-(.+)$", value)
    if not m:
        raise typer.BadParameter(f"expected YEAR-ROUND, got {value!r}")
    return int(m.group(1)), m.group(2)


def _parse_speed(value: str) -> float:
    """`1x`, `0.5x`, `5x` -> float."""
    if not value.endswith("x"):
        raise typer.BadParameter(f"speed must end in 'x' (e.g. 1x, 0.5x), got {value!r}")
    try:
        return float(value[:-1])
    except ValueError as e:
        raise typer.BadParameter(str(e))


def _parse_bound(value: Optional[str]) -> Optional[timedelta]:
    """`HH:MM:SS` -> timedelta from session start. (`lap N` is a workshop item.)"""
    if value is None:
        return None
    m = re.match(r"^(\d{1,2}):(\d{2}):(\d{2})$", value)
    if m:
        return timedelta(hours=int(m.group(1)), minutes=int(m.group(2)), seconds=int(m.group(3)))
    if value.startswith("lap "):
        raise typer.BadParameter("lap-relative bounds not implemented yet")
    raise typer.BadParameter(f"expected HH:MM:SS or 'lap N', got {value!r}")


@app.command()
def replay(
    session: str = typer.Option(..., "--session", help="Session id, YEAR-ROUND (e.g. 2026-canada)"),
    endpoint: str = typer.Option("localhost:4317", "--endpoint", help="OTLP gRPC endpoint"),
    speed: str = typer.Option("1x", "--speed", help="Playback speed multiplier (e.g. 1x, 5x)"),
    dump: bool = typer.Option(False, "--dump", help="Emit everything as fast as possible, no pacing"),
    start: Optional[str] = typer.Option(None, "--from", help="Start point: HH:MM:SS or 'lap N'"),
    stop: Optional[str] = typer.Option(None, "--to", help="End point: HH:MM:SS or 'lap N'"),
    drivers_arg: Optional[str] = typer.Option(None, "--driver", help="Comma-separated driver codes (e.g. VER,NOR). Default: all."),
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Debug logging"),
) -> None:
    """Replay an F1 session as OTLP traces/metrics/logs."""
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(asctime)s %(levelname)-5s %(name)s: %(message)s",
    )

    year, round_name = _parse_session(session)
    speed_x = _parse_speed(speed)
    start_at = _parse_bound(start) or timedelta(0)
    end_at = _parse_bound(stop)
    driver_filter: Optional[List[str]] = (
        [d.strip().upper() for d in drivers_arg.split(",")] if drivers_arg else None
    )

    session_mod.configure_cache()
    fastf1_session = session_mod.load_race(year, round_name)
    info = session_mod.extract_session_info(fastf1_session)
    drivers = session_mod.extract_drivers(fastf1_session)

    if driver_filter is not None:
        drivers = [d for d in drivers if d.code in driver_filter]
        if not drivers:
            raise typer.BadParameter(f"no drivers matched filter: {driver_filter}")

    typer.echo(
        f"Loaded {info.year} {info.round_name} {info.session_type}: "
        f"{len(drivers)} drivers, duration {info.duration}."
    )

    bundles = make_provider_bundles(drivers, info, endpoint)
    runtimes = build_runtimes(drivers)

    # Anchor race_t = 0 to wall-clock now, so spans land live in axolot(e)l.
    wall_start = datetime.now().astimezone()
    emitters = make_emitters(bundles, wall_start)

    config = ReplayConfig(speed=speed_x, dump=dump, start_at=start_at, end_at=end_at)
    engine = ReplayEngine(info, runtimes, emitters, config)

    try:
        engine.run()
    finally:
        for b in bundles.values():
            b.shutdown()

    typer.echo("Done.")
