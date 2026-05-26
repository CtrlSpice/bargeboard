/**
 * Smoke test — emit a small set of spans + a metric + a log, then shut down
 * the providers cleanly so the batch processors actually flush.
 *
 * Run:   npx tsx scripts/smoke.ts
 *        OTLP_ENDPOINT=host:port npx tsx scripts/smoke.ts   (defaults to localhost:4317)
 */
import { ROOT_CONTEXT, trace } from "@opentelemetry/api";
import { SeverityNumber } from "@opentelemetry/api-logs";
import { SCOPE_NAME, SCOPE_VERSION } from "../src/emit/constants.js";
import type { DriverInfo, SessionInfo } from "../src/models.js";
import { makeDriverBundle, makeSessionBundle } from "../src/providers.js";
import { raceTimeToUnixNanos } from "../src/util.js";

const endpoint = process.env.OTLP_ENDPOINT ?? "localhost:4317";
const targets = { endpoint, dryRun: false, consoleEcho: false };

const session: SessionInfo = {
  year: 2026,
  round_name: "Canada",
  session_type: "Race",
  start_time: new Date("2026-05-24T18:00:00Z"),
  duration_s: 60,
  session_key: 11291,
};

const driver: DriverInfo = {
  code: "ANT",
  full_name: "Kimi Antonelli",
  team: "Mercedes",
  car_number: 12,
};

async function main(): Promise<void> {
  const sb = makeSessionBundle(session, targets);
  const db = makeDriverBundle(driver, targets);

  const sTracer = sb.tracerProvider.getTracer(SCOPE_NAME, SCOPE_VERSION);
  const dTracer = db.tracerProvider.getTracer(SCOPE_NAME, SCOPE_VERSION);
  const dMeter = db.meterProvider.getMeter(SCOPE_NAME, SCOPE_VERSION);
  const dLogger = db.loggerProvider.getLogger(SCOPE_NAME, SCOPE_VERSION);

  const wallStart = session.start_time;
  const t0 = Number(raceTimeToUnixNanos(wallStart, 0) / 1_000_000n);   // ms for span APIs
  const t30 = Number(raceTimeToUnixNanos(wallStart, 30) / 1_000_000n);
  const t60 = Number(raceTimeToUnixNanos(wallStart, 60) / 1_000_000n);

  const root = sTracer.startSpan("2026_canada_race_smoke", { startTime: t0 });
  const rootCtx = trace.setSpan(ROOT_CONTEXT, root);

  const raceSpan = dTracer.startSpan(
    "ANT_race_smoke",
    { startTime: t0 },
    rootCtx,
  );
  const raceCtx = trace.setSpan(rootCtx, raceSpan);

  const lapSpan = dTracer.startSpan(
    "ANT_L1_smoke",
    { startTime: t0, attributes: { "f1.lap.number": 1 } },
    raceCtx,
  );
  lapSpan.addEvent("smoke_marker", { "f1.note": "hello from bargeboard" }, t30);
  lapSpan.end(t60);

  const speed = dMeter.createGauge("f1.car.speed", { unit: "km/h" });
  speed.record(280);

  dLogger.emit({
    severityNumber: SeverityNumber.INFO,
    severityText: "INFO",
    body: "smoke test log line",
    attributes: { "f1.driver.code": "ANT" },
  });

  raceSpan.end(t60);
  root.end(t60);

  await Promise.all([sb.shutdown(), db.shutdown()]);
  console.log(`smoke emitted to ${endpoint} and flushed.`);
}

main().catch((e) => {
  console.error("smoke failed:", e);
  process.exit(1);
});
