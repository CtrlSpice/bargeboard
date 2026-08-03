/**
 * Build TracerProvider + MeterProvider + LoggerProvider bundles.
 *
 * Per-driver bundles (traces + logs only) share a team `service.name` but
 * differ on `service.instance.id`. The session bundle owns the race-root
 * span and ALL metrics — every instrument lives on the single "race"
 * resource with `f1.driver.code` / `f1.team` as datapoint attributes, so
 * cross-driver queries never span resources.
 *
 * Metrics deliberately bypass the SDK MeterProvider: datapoints need
 * historical race timestamps, which the SDK can't produce. The session
 * bundle exposes the raw OTLPMetricExporter; emit/metrics.ts (MetricBank)
 * builds the datapoints.
 *
 * No providers are registered globally — every emitter takes its bundle by
 * reference. Shutdown flushes traces/logs/metrics in that order.
 */

import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-grpc";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-grpc";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-grpc";
import {
  LoggerProvider,
  BatchLogRecordProcessor,
  ConsoleLogRecordExporter,
  SimpleLogRecordProcessor,
  type LogRecordExporter,
} from "@opentelemetry/sdk-logs";
import {
  NodeTracerProvider,
} from "@opentelemetry/sdk-trace-node";
import type { Resource } from "@opentelemetry/resources";
import {
  BatchSpanProcessor,
  ConsoleSpanExporter,
  SimpleSpanProcessor,
  type SpanExporter,
} from "@opentelemetry/sdk-trace-base";

import type { DriverInfo, SessionInfo } from "./models.js";
import { makeDriverResource, makeSessionResource } from "./resource.js";

export interface ExportTargets {
  endpoint: string;       // "host:port" for OTLP gRPC
  dryRun: boolean;        // true => no exporters, no network
  consoleEcho: boolean;   // true => add console exporters too (for inspection)
}

export interface DriverBundle {
  driver: DriverInfo;
  tracerProvider: NodeTracerProvider;
  loggerProvider: LoggerProvider;
  shutdown: () => Promise<void>;
}

export interface SessionBundle {
  session: SessionInfo;
  resource: Resource;
  tracerProvider: NodeTracerProvider;
  /** Raw OTLP metric exporter for the MetricBank (null in dry-run). The SDK
   *  MeterProvider pipeline is deliberately absent — it stamps datapoints at
   *  collection time, and we need historical race timestamps. */
  metricExporter: OTLPMetricExporter | null;
  shutdown: () => Promise<void>;
}

function makeTraceExporter(t: ExportTargets): SpanExporter | null {
  if (t.dryRun) return null;
  return new OTLPTraceExporter({ url: `http://${t.endpoint}` });
}

function makeMetricExporter(t: ExportTargets): OTLPMetricExporter | null {
  if (t.dryRun) return null;
  return new OTLPMetricExporter({ url: `http://${t.endpoint}` });
}

function makeLogExporter(t: ExportTargets): LogRecordExporter | null {
  if (t.dryRun) return null;
  return new OTLPLogExporter({ url: `http://${t.endpoint}` });
}

export function makeSessionBundle(session: SessionInfo, t: ExportTargets): SessionBundle {
  const resource = makeSessionResource(session);

  const tp = new NodeTracerProvider({ resource });
  const trExp = makeTraceExporter(t);
  if (trExp) tp.addSpanProcessor(new BatchSpanProcessor(trExp));
  if (t.consoleEcho) tp.addSpanProcessor(new SimpleSpanProcessor(new ConsoleSpanExporter()));

  const metricExporter = makeMetricExporter(t);

  return {
    session,
    resource,
    tracerProvider: tp,
    metricExporter,
    shutdown: async () => {
      await tp.shutdown();
      if (metricExporter) await metricExporter.shutdown();
    },
  };
}

export function makeDriverBundle(driver: DriverInfo, t: ExportTargets): DriverBundle {
  const resource = makeDriverResource(driver);

  const tp = new NodeTracerProvider({ resource });
  const trExp = makeTraceExporter(t);
  if (trExp) tp.addSpanProcessor(new BatchSpanProcessor(trExp));
  if (t.consoleEcho) tp.addSpanProcessor(new SimpleSpanProcessor(new ConsoleSpanExporter()));

  const lp = new LoggerProvider({ resource });
  const lExp = makeLogExporter(t);
  if (lExp) lp.addLogRecordProcessor(new BatchLogRecordProcessor(lExp));
  if (t.consoleEcho) lp.addLogRecordProcessor(new SimpleLogRecordProcessor(new ConsoleLogRecordExporter()));

  return {
    driver,
    tracerProvider: tp,
    loggerProvider: lp,
    shutdown: async () => {
      await tp.shutdown();
      await lp.shutdown();
    },
  };
}

export function makeDriverBundles(
  drivers: DriverInfo[],
  t: ExportTargets,
): Map<string, DriverBundle> {
  const out = new Map<string, DriverBundle>();
  for (const d of drivers) out.set(d.code, makeDriverBundle(d, t));
  return out;
}
