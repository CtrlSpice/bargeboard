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
import { DriverInterpreter, SessionInterpreter } from "./emit/effects.js";
import {
  closeDriverRace,
  closeSessionRoot,
  handle,
  openDriverRace,
  openSessionRoot,
} from "./emit/handlers.js";
import { initialDriverState, type DriverState } from "./emit/state.js";
import { log, sleep } from "./util.js";

const TICK_MS = 100;

export interface ReplayConfig {
  speed: number;
  dump: boolean;
  startAt: number;        // seconds
  endAt: number;          // seconds
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
  perDriverQueues: Map<string, Event[]>;
  config: ReplayConfig;
}

export async function runReplay(ctx: ReplayContext): Promise<void> {
  const sessionInterp = new SessionInterpreter(
    ctx.sessionBundle,
    ctx.session.start_time,
    ctx.session,
  );

  // Build per-driver runtimes.
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

  // Open the session root, then each driver's race span.
  const rootName = `${ctx.session.year}_${ctx.session.round_name.toLowerCase().replace(/\s+/g, "_")}_${ctx.session.session_type.toLowerCase()}`;
  for (const eff of openSessionRoot(rootName, ctx.config.startAt)) {
    sessionInterp.apply(eff);
  }
  for (const rt of runtimes) {
    const { state: stateUpdate, effects } = openDriverRace(rt.info, ctx.config.startAt);
    for (const eff of effects) rt.interp.apply(eff);
    rt.state = { ...rt.state, ...stateUpdate };
  }

  // Tick loop.
  let raceT = ctx.config.startAt;
  const wallStart = Date.now();
  const tickSec = TICK_MS / 1000;

  try {
    while (raceT < ctx.config.endAt) {
      raceT = Math.min(raceT + tickSec * ctx.config.speed, ctx.config.endAt);
      tick(runtimes, raceT);

      if (!ctx.config.dump) {
        const targetWall = wallStart + ((raceT - ctx.config.startAt) / ctx.config.speed) * 1000;
        const sleepFor = targetWall - Date.now();
        if (sleepFor > 0) await sleep(sleepFor);
      }
    }
  } finally {
    // Close every driver, then the session root.
    for (const rt of runtimes) {
      const effects = closeDriverRace(rt.state, raceT);
      for (const eff of effects) rt.interp.apply(eff);
    }
    for (const eff of closeSessionRoot(raceT)) {
      sessionInterp.apply(eff);
    }
    log.info(`Replay finished at race_t=${raceT.toFixed(1)}s`);
  }
}

function tick(runtimes: DriverRuntime[], raceT: number): void {
  for (const rt of runtimes) {
    if (rt.finished) continue;
    while (rt.pos < rt.queue.length && rt.queue[rt.pos]!.race_t <= raceT) {
      const ev = rt.queue[rt.pos]!;
      rt.pos++;
      const result = handle(rt.state, ev, rt.info);
      rt.state = result.state;
      for (const eff of result.effects) rt.interp.apply(eff);
    }
  }
}
