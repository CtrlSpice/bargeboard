/**
 * Commander CLI for bargeboard.
 *
 * Exactly one of --session or --season. Paced playback is single-session; the
 * --season mode requires --dump because pacing 24 races back-to-back doesn't
 * make sense.
 */

import { Command, InvalidArgumentError } from "commander";

import { loadDrivers, loadEvents, loadSessionInfo } from "./extract.js";
import { fanOut } from "./fanout.js";
import { makeDriverBundles, makeSessionBundle, type ExportTargets } from "./providers.js";
import { runReplay, type ReplayConfig } from "./replay.js";
import { log, setVerbose } from "./util.js";
import type { DriverInfo, Event, SessionInfo } from "./models.js";

interface CliOpts {
  session?: string;
  season?: string;
  endpoint: string;
  speed: string;
  dump?: boolean;
  dryRun?: boolean;
  from?: string;
  to?: string;
  driver?: string;
  verbose?: boolean;
}

function parseSessionId(value: string): { year: number; round: string } {
  const m = value.match(/^(\d{4})-(.+)$/);
  if (!m) throw new InvalidArgumentError(`expected YEAR-ROUND, got ${value}`);
  return { year: Number(m[1]), round: m[2]! };
}

function parseSpeed(value: string): number {
  if (!value.endsWith("x")) throw new InvalidArgumentError(`speed must end in 'x', got ${value}`);
  const n = Number(value.slice(0, -1));
  if (!Number.isFinite(n) || n <= 0) throw new InvalidArgumentError(`bad speed: ${value}`);
  return n;
}

function parseBound(value: string | undefined): number | null {
  if (!value) return null;
  const m = value.match(/^(\d{1,2}):(\d{2}):(\d{2})$/);
  if (!m) throw new InvalidArgumentError(`expected HH:MM:SS, got ${value}`);
  return Number(m[1]) * 3600 + Number(m[2]) * 60 + Number(m[3]);
}

export function buildProgram(): Command {
  const program = new Command();
  program
    .name("bargeboard")
    .description("Replay F1 race sessions as OpenTelemetry signals.")
    .option("--session <id>", "Single session id, YEAR-ROUND (e.g. 2026-canada)")
    .option("--season <year>", "Replay every race of a season. Requires --dump.")
    .option("--endpoint <url>", "OTLP gRPC endpoint", "localhost:4317")
    .option("--speed <n>", "Playback speed multiplier (e.g. 1x, 5x)", "1x")
    .option("--dump", "Emit everything as fast as possible, no pacing.")
    .option("--dry-run", "Build everything but skip OTLP export (no network).")
    .option("--from <hms>", "Start point: HH:MM:SS")
    .option("--to <hms>", "End point: HH:MM:SS")
    .option("--driver <codes>", "Comma-separated driver codes (e.g. VER,NOR)")
    .option("-v, --verbose", "Debug logging")
    .action(async (opts: CliOpts) => {
      setVerbose(Boolean(opts.verbose));

      if (Boolean(opts.session) === Boolean(opts.season)) {
        throw new InvalidArgumentError("pass exactly one of --session or --season");
      }
      if (opts.season && !opts.dump) {
        throw new InvalidArgumentError("--season requires --dump");
      }

      const speed = parseSpeed(opts.speed);
      const startAt = parseBound(opts.from) ?? 0;
      const endBound = parseBound(opts.to);
      const driverFilter = opts.driver
        ? new Set(opts.driver.split(",").map((s) => s.trim().toUpperCase()))
        : undefined;

      const targets: ExportTargets = {
        endpoint: opts.endpoint,
        dryRun: Boolean(opts.dryRun),
        consoleEcho: false,
      };

      if (opts.session) {
        const { year, round } = parseSessionId(opts.session);
        await runOneRace({
          year, round, targets, speed,
          dump: Boolean(opts.dump),
          startAt, endBound, driverFilter,
        });
      } else if (opts.season) {
        // Season mode just loops every race that has a /sessions entry for the year.
        // Kept simple: no parallelism, restart providers between races.
        throw new Error("--season is not yet implemented in the TS rewrite");
      }
    });
  return program;
}

interface RaceRunOpts {
  year: number;
  round: string;
  targets: ExportTargets;
  speed: number;
  dump: boolean;
  startAt: number;
  endBound: number | null;
  driverFilter: Set<string> | undefined;
}

async function runOneRace(o: RaceRunOpts): Promise<void> {
  log.info(`Looking up ${o.year} ${o.round} race in OpenF1...`);
  const session: SessionInfo = await loadSessionInfo(o.year, o.round);
  log.info(`Session ${session.session_key}: ${session.year} ${session.round_name} ${session.session_type}, start=${session.start_time.toISOString()}, duration=${session.duration_s.toFixed(0)}s`);

  const { drivers, numberToCode } = await loadDrivers(session.session_key);
  const filtered: DriverInfo[] = o.driverFilter
    ? drivers.filter((d) => o.driverFilter!.has(d.code))
    : drivers;
  if (filtered.length === 0) {
    log.warn("No drivers matched the filter; nothing to do.");
    return;
  }
  log.info(`Drivers: ${filtered.map((d) => d.code).join(", ")} (${filtered.length})`);

  const { events, raceStartT, grid } = await loadEvents(session, drivers, numberToCode, {
    driverFilter: o.driverFilter,
  });
  log.info(`Total events: ${events.length}`);
  reportEventCounts(events);

  const { formation, perDriver } = fanOut(events, filtered.map((d) => d.code), raceStartT);
  if (formation.length > 0) {
    log.info(`Formation phase: ${formation.length} pre-race events on the formation span.`);
  }

  const sessionBundle = makeSessionBundle(session, o.targets);
  const driverBundles = makeDriverBundles(filtered, o.targets);

  const config: ReplayConfig = {
    speed: o.speed,
    dump: o.dump,
    startAt: o.startAt,
    endAt: o.endBound ?? session.duration_s,
    raceStartT,
  };

  try {
    await runReplay({
      session,
      drivers: filtered,
      driverBundles,
      sessionBundle,
      formationQueue: formation,
      perDriverQueues: perDriver,
      grid,
      config,
    });
  } finally {
    for (const b of driverBundles.values()) await b.shutdown();
    await sessionBundle.shutdown();
  }
}

function reportEventCounts(events: Event[]): void {
  const counts: Record<string, number> = {};
  for (const e of events) counts[e.kind] = (counts[e.kind] ?? 0) + 1;
  const summary = Object.entries(counts)
    .sort((a, b) => b[1] - a[1])
    .map(([k, v]) => `${k}=${v}`)
    .join(" ");
  log.info(`Event breakdown: ${summary}`);
}
