/**
 * Tick-driven replay engine.
 *
 * Fixed 100ms wall-clock ticks. Each tick advances the race clock by
 * `100ms × speed`. Events with race_t <= current race_t are drained from each
 * driver's queue and dispatched through the pure handlers; effects are
 * applied by the per-driver interpreter.
 *
 * `--dump` mode: skip the sleep, run as fast as possible. Useful for season
 * replay or for verifying telemetry flows.
 */

import type { DriverInfo, Event, SessionInfo } from "./models.js";
import type { DriverBundle, SessionBundle } from "./providers.js";
import { DriverInterpreter, SessionInterpreter, type Effect } from "./emit/effects.js";
import {
  closeDriverRace,
  closeSessionRoot,
  flushTelemetryGauges,
  handle,
  handleFormation,
  handleSessionEvent,
  openDriverRace,
  openSessionRoot,
} from "./emit/handlers.js";
import { FLUSH_EVERY_S } from "./emit/metrics.js";
import { initialDriverState, type DriverState } from "./emit/state.js";
import { log, sleep } from "./util.js";

const TICK_MS = 100;

export interface ReplayConfig {
  speed: number;
  dump: boolean;
  startAt: number;        // seconds
  endAt: number;          // seconds
  /** Lights-out moment. The engine spends 0..raceStartT in the formation
   *  phase (session root + driver race spans NOT yet open; pre-race events
   *  land on the root as span events) and raceStartT..endAt in the race
   *  phase. If 0, the race begins immediately. */
  raceStartT: number;
}

interface DriverRuntime {
  info: DriverInfo;
  state: DriverState;
  queue: Event[];
  pos: number;            // next un-dispatched index
  interp: DriverInterpreter;
  finished: boolean;
}

export interface ReplayContext {
  session: SessionInfo;
  drivers: DriverInfo[];
  driverBundles: Map<string, DriverBundle>;
  sessionBundle: SessionBundle;
  /** Pre-race events (race_t < config.raceStartT) handled by the session
   *  interpreter as span events on the session root. */
  formationQueue: Event[];
  /** Session-scoped events (weather) drained every tick in both phases,
   *  applied to the session interpreter without driver attributes. */
  sessionQueue: Event[];
  /** Race-phase events per driver (race_t >= config.raceStartT). */
  perDriverQueues: Map<string, Event[]>;
  /** Starting grid: driver code → position (1 = pole). Drivers not in the
   *  map fall back to position grid.size+1 (back of the grid). */
  grid: Map<string, number>;
  config: ReplayConfig;
}

/** Per-position stagger applied to driver race-span start times so traces
 *  display in grid order. Small enough to be visually invisible (~22ms
 *  total spread across the field), large enough to give every span a
 *  distinct start nanosecond. */
const GRID_STAGGER_S = 0.001;

export async function runReplay(ctx: ReplayContext): Promise<void> {
  const sessionInterp = new SessionInterpreter(
    ctx.sessionBundle,
    ctx.session.start_time,
    ctx.session,
  );

  const runtimes: DriverRuntime[] = ctx.drivers.map((d) => {
    const bundle = ctx.driverBundles.get(d.code)!;
    const interp = new DriverInterpreter(bundle, ctx.session.start_time, sessionInterp);
    return {
      info: d,
      state: initialDriverState(d.code),
      queue: ctx.perDriverQueues.get(d.code) ?? [],
      pos: 0,
      interp,
      finished: false,
    };
  });

  // Open the session root at LIGHTS-OUT, not session start: the race trace
  // begins at t=0 of the actual race. Driver race spans don't open until the
  // race phase begins at raceStartT either — pre-race events attach to the
  // root as span events (with timestamps preceding the span start).
  const rootName = `${ctx.session.year}_${ctx.session.round_name.toLowerCase().replace(/\s+/g, "_")}_${ctx.session.session_type.toLowerCase()}`;
  const rootOpenT = Math.max(ctx.config.startAt, ctx.config.raceStartT);
  apply(sessionInterp, openSessionRoot(rootName, rootOpenT));
  const hasFormation = ctx.config.raceStartT > ctx.config.startAt;

  // Cumulative metric series (sums, histograms) count from lights-out.
  sessionInterp.bank.setStartTime(Math.max(ctx.config.startAt, ctx.config.raceStartT));

  let raceT = ctx.config.startAt;
  let phase: "formation" | "race" = hasFormation ? "formation" : "race";
  let formationPos = 0;
  let sessionPos = 0;
  let nextFlushT = ctx.config.startAt + FLUSH_EVERY_S;
  if (phase === "race") openAllDriverRaces(runtimes, ctx.config.raceStartT, ctx.grid);

  const wallStart = Date.now();
  const tickSec = TICK_MS / 1000;

  try {
    while (raceT < ctx.config.endAt) {
      raceT = Math.min(raceT + tickSec * ctx.config.speed, ctx.config.endAt);

      if (phase === "formation") {
        formationPos = drainFormation(ctx.formationQueue, formationPos, raceT, sessionInterp);
        if (raceT >= ctx.config.raceStartT) {
          // Drain any remaining formation events exactly at the boundary, then transition.
          formationPos = drainFormation(ctx.formationQueue, formationPos, ctx.config.raceStartT, sessionInterp);
          openAllDriverRaces(runtimes, ctx.config.raceStartT, ctx.grid);
          phase = "race";
        }
      } else {
        tick(runtimes, raceT);
      }

      // Session-scoped events (weather) drain in both phases.
      sessionPos = drainSession(ctx.sessionQueue, sessionPos, raceT, sessionInterp);

      // Metric datapoints flush on a fixed race-time cadence, stamped with
      // historical timestamps — this is what keeps metrics on the race's
      // time axis instead of the replay's.
      while (raceT >= nextFlushT) {
        sessionInterp.bank.flush(nextFlushT);
        sessionInterp.bank.export();
        nextFlushT += FLUSH_EVERY_S;
      }

      if (!ctx.config.dump) {
        const targetWall = wallStart + ((raceT - ctx.config.startAt) / ctx.config.speed) * 1000;
        const sleepFor = targetWall - Date.now();
        if (sleepFor > 0) await sleep(sleepFor);
      } else {
        // Yield so the OTel SDK's setTimeout-driven batch exporters can drain
        // their queues — otherwise spans/logs pile up and overflow.
        await new Promise<void>((r) => setImmediate(r));
      }
    }
  } finally {
    // Close driver spans for anyone whose race didn't wrap up via
    // Retirement/RaceFinish (no-op for drivers whose races never opened,
    // e.g. when --to clipped before lights-out).
    for (const rt of runtimes) {
      const effects = closeDriverRace(rt.state, raceT);
      for (const eff of effects) rt.interp.apply(eff);
    }
    // Root opened at lights-out; never close it before its own start.
    apply(sessionInterp, closeSessionRoot(Math.max(raceT, rootOpenT)));
    // Final metric flush + wait for in-flight OTLP exports before the
    // caller shuts the exporter down.
    sessionInterp.bank.flush(raceT);
    sessionInterp.bank.export();
    await sessionInterp.bank.drain();
    log.info(`Replay finished at race_t=${raceT.toFixed(1)}s`);
  }
}

function apply(interp: SessionInterpreter, effects: Effect[]): void {
  for (const e of effects) interp.apply(e);
}

function openAllDriverRaces(
  runtimes: DriverRuntime[],
  raceT: number,
  grid: Map<string, number>,
): void {
  // Drivers without a grid entry get sent to the back so we never collide
  // two spans on the same nanosecond.
  const backOfGrid = grid.size + 1;
  for (const rt of runtimes) {
    const position = grid.get(rt.info.code) ?? backOfGrid;
    const staggeredT = raceT + (position - 1) * GRID_STAGGER_S;
    const { state: stateUpdate, effects } = openDriverRace(rt.info, staggeredT);
    for (const eff of effects) rt.interp.apply(eff);
    rt.state = { ...rt.state, ...stateUpdate };
  }
}

function drainFormation(
  queue: Event[],
  pos: number,
  raceT: number,
  interp: SessionInterpreter,
): number {
  while (pos < queue.length && queue[pos]!.race_t <= raceT) {
    const effects = handleFormation(queue[pos]!);
    for (const eff of effects) interp.apply(eff);
    pos++;
  }
  return pos;
}

function drainSession(
  queue: Event[],
  pos: number,
  raceT: number,
  interp: SessionInterpreter,
): number {
  while (pos < queue.length && queue[pos]!.race_t <= raceT) {
    const effects = handleSessionEvent(queue[pos]!);
    for (const eff of effects) interp.apply(eff);
    pos++;
  }
  return pos;
}

function tick(runtimes: DriverRuntime[], raceT: number): void {
  for (const rt of runtimes) {
    if (rt.state.raceClosed) continue;
    while (rt.pos < rt.queue.length && rt.queue[rt.pos]!.race_t <= raceT) {
      const ev = rt.queue[rt.pos]!;
      rt.pos++;
      const result = handle(rt.state, ev, rt.info);
      rt.state = result.state;
      for (const eff of result.effects) rt.interp.apply(eff);
    }
    // After draining the tick's events, flush a single batch of gauge effects
    // from the most recent Telemetry seen (if any). Gauges are LastValue, so
    // any intervening samples in this tick window were already collapsed.
    const flush = flushTelemetryGauges(rt.state);
    if (flush.effects.length > 0) {
      rt.state = flush.state;
      for (const eff of flush.effects) rt.interp.apply(eff);
    }
  }
}
