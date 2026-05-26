/**
 * Tiny utilities. No fp-ts; just enough plumbing.
 */

/** Left-to-right function composition. `pipe(x, f, g)` === `g(f(x))`. */
export function pipe<A, B>(a: A, f1: (a: A) => B): B;
export function pipe<A, B, C>(a: A, f1: (a: A) => B, f2: (b: B) => C): C;
export function pipe<A, B, C, D>(a: A, f1: (a: A) => B, f2: (b: B) => C, f3: (c: C) => D): D;
export function pipe(a: unknown, ...fns: Array<(x: unknown) => unknown>): unknown {
  return fns.reduce((acc, f) => f(acc), a);
}

/** Sleep N milliseconds. */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Lightweight logger (no console.log scattered through the code). */
export interface Logger {
  debug: (msg: string, ...args: unknown[]) => void;
  info: (msg: string, ...args: unknown[]) => void;
  warn: (msg: string, ...args: unknown[]) => void;
  error: (msg: string, ...args: unknown[]) => void;
}

let verbose = false;
export function setVerbose(v: boolean): void {
  verbose = v;
}

export const log: Logger = {
  debug: (m, ...a) => { if (verbose) process.stderr.write(`[debug] ${fmt(m, a)}\n`); },
  info: (m, ...a) => { process.stderr.write(`[info ] ${fmt(m, a)}\n`); },
  warn: (m, ...a) => { process.stderr.write(`[warn ] ${fmt(m, a)}\n`); },
  error: (m, ...a) => { process.stderr.write(`[error] ${fmt(m, a)}\n`); },
};

function fmt(msg: string, args: unknown[]): string {
  if (args.length === 0) return msg;
  return msg + " " + args.map((a) => (typeof a === "string" ? a : JSON.stringify(a))).join(" ");
}

/** Wall-clock Unix nanoseconds for a given Date. */
export function toUnixNanos(d: Date): bigint {
  return BigInt(d.getTime()) * 1_000_000n;
}

/** Wall-clock Unix nanos for `session_start + race_t (seconds)`. */
export function raceTimeToUnixNanos(sessionStart: Date, raceT: number): bigint {
  const ms = sessionStart.getTime() + raceT * 1000;
  // Preserve sub-millisecond precision by computing nanos directly.
  const wholeMs = Math.floor(ms);
  const frac = ms - wholeMs;
  return BigInt(wholeMs) * 1_000_000n + BigInt(Math.round(frac * 1_000_000));
}
