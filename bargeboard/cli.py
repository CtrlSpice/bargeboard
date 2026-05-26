"""Typer CLI for bargeboard."""
from __future__ import annotations

import logging
import re
from datetime import timedelta
from typing import List, Optional, Tuple

import typer

from bargeboard import extract, session as session_mod
from bargeboard.emit import SessionEmitter, make_emitters
from bargeboard.providers import make_provider_bundles, make_session_bundle
from bargeboard.replay import ReplayConfig, ReplayEngine, build_runtimes, fan_out_events

app = typer.Typer(add_completion=False, help="Replay F1 race sessions as OpenTelemetry signals.")

log = logging.getLogger(__name__)


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


def _run_one_race(
    year: int,
    round_id,
    endpoint: str,
    speed_x: float,
    dump: bool,
    start_at: timedelta,
    end_at: Optional[timedelta],
    driver_filter: Optional[List[str]],
) -> None:
    """Load and replay a single race. Providers are built and shut down per
    race so seasonal replays don't leak state across events."""
    fastf1_session = session_mod.load_race(year, round_id)
    info = session_mod.extract_session_info(fastf1_session)
    drivers = session_mod.extract_drivers(fastf1_session)

    if driver_filter is not None:
        drivers = [d for d in drivers if d.code in driver_filter]
        if not drivers:
            typer.echo(f"  skipping {info.round_name}: no drivers matched filter {driver_filter}")
            return

    typer.echo(
        f"Loaded {info.year} {info.round_name} {info.session_type}: "
        f"{len(drivers)} drivers, duration {info.duration}."
    )

    bundles = make_provider_bundles(drivers, info, endpoint)
    session_bundle = make_session_bundle(info, endpoint)

    events = extract.extract_events(fastf1_session)
    per_driver_queues = fan_out_events(events, [d.code for d in drivers])
    runtimes = build_runtimes(drivers, per_driver_queues)

    # Anchor race_t = 0 to the actual session start time. Spans get emitted
    # live (paced by the tick loop) but stamped with the historical race date,
    # so axolot(e)l's time selector navigates to the real race window.
    wall_start = info.start_time
    session_emitter = SessionEmitter(session_bundle, wall_start)
    emitters = make_emitters(bundles, wall_start, session_emitter)

    config = ReplayConfig(speed=speed_x, dump=dump, start_at=start_at, end_at=end_at)
    engine = ReplayEngine(info, session_emitter, runtimes, emitters, config)

    try:
        engine.run()
    finally:
        for b in bundles.values():
            b.shutdown()
        session_bundle.shutdown()


@app.command()
def replay(
    session: Optional[str] = typer.Option(None, "--session", help="Single session id, YEAR-ROUND (e.g. 2026-canada). Required unless --season is set."),
    season: Optional[int] = typer.Option(None, "--season", help="Replay every completed race in a season. Requires --dump (paced playback is single-session only)."),
    endpoint: str = typer.Option("localhost:4317", "--endpoint", help="OTLP gRPC endpoint"),
    speed: str = typer.Option("1x", "--speed", help="Playback speed multiplier (e.g. 1x, 5x). Ignored when --dump is set."),
    dump: bool = typer.Option(False, "--dump", help="Emit everything as fast as possible, no pacing."),
    start: Optional[str] = typer.Option(None, "--from", help="Start point: HH:MM:SS or 'lap N'"),
    stop: Optional[str] = typer.Option(None, "--to", help="End point: HH:MM:SS or 'lap N'"),
    drivers_arg: Optional[str] = typer.Option(None, "--driver", help="Comma-separated driver codes (e.g. VER,NOR). Default: all."),
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Debug logging"),
) -> None:
    """Replay an F1 session (or, in --dump mode, a whole season) as OTLP traces/metrics/logs."""
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(asctime)s %(levelname)-5s %(name)s: %(message)s",
    )

    if (session is None) == (season is None):
        raise typer.BadParameter("pass exactly one of --session or --season")
    if season is not None and not dump:
        raise typer.BadParameter("--season requires --dump (paced playback is capped at one session)")

    speed_x = _parse_speed(speed)
    start_at = _parse_bound(start) or timedelta(0)
    end_at = _parse_bound(stop)
    driver_filter: Optional[List[str]] = (
        [d.strip().upper() for d in drivers_arg.split(",")] if drivers_arg else None
    )

    session_mod.configure_cache()

    if session is not None:
        year, round_id = _parse_session(session)
        _run_one_race(year, round_id, endpoint, speed_x, dump, start_at, end_at, driver_filter)
    else:
        assert season is not None
        races = session_mod.list_season_races(season)
        if not races:
            raise typer.BadParameter(f"no completed races found for season {season}")
        typer.echo(f"Season {season}: {len(races)} completed race(s) to dump.")
        for round_number, event_name in races:
            typer.echo(f"--- Round {round_number}: {event_name} ---")
            try:
                _run_one_race(season, round_number, endpoint, speed_x, dump, start_at, end_at, driver_filter)
            except Exception as e:
                log.exception("round %d (%s) failed: %s", round_number, event_name, e)

    typer.echo("Done.")
