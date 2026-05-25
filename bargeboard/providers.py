"""Per-car OpenTelemetry provider construction.

One car = one OTel Resource = one TracerProvider + MeterProvider + LoggerProvider.
Cars on the same team share `service.name` but differ on `service.instance.id`, so
axolot(e)l groups them together while still distinguishing the two drivers.

We do not register any provider globally (no `trace.set_tracer_provider`). Providers
are kept in a `ProviderBundle` per car and passed explicitly to the emitter.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Dict

from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk._logs import LoggerProvider
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

from bargeboard.models import DriverInfo, SessionInfo


@dataclass
class ProviderBundle:
    driver: DriverInfo
    tracer_provider: TracerProvider
    meter_provider: MeterProvider
    logger_provider: LoggerProvider

    def shutdown(self) -> None:
        """Flush and close all three providers."""
        self.tracer_provider.shutdown()
        self.meter_provider.shutdown()
        self.logger_provider.shutdown()


def make_resource(driver: DriverInfo, session: SessionInfo) -> Resource:
    return Resource.create(
        {
            "service.name": driver.team,
            "service.instance.id": driver.code,
            "f1.driver.code": driver.code,
            "f1.driver.full_name": driver.full_name,
            "f1.car.number": driver.car_number,
            "f1.team": driver.team,
            "f1.session.year": session.year,
            "f1.session.round": session.round_name,
            "f1.session.type": session.session_type,
        }
    )


def make_provider_bundle(
    driver: DriverInfo,
    session: SessionInfo,
    endpoint: str,
) -> ProviderBundle:
    resource = make_resource(driver, session)

    # Traces
    tp = TracerProvider(resource=resource)
    tp.add_span_processor(
        BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint, insecure=True))
    )

    # Metrics
    metric_reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=endpoint, insecure=True),
        export_interval_millis=1000,
    )
    mp = MeterProvider(resource=resource, metric_readers=[metric_reader])

    # Logs
    lp = LoggerProvider(resource=resource)
    lp.add_log_record_processor(
        BatchLogRecordProcessor(OTLPLogExporter(endpoint=endpoint, insecure=True))
    )

    return ProviderBundle(
        driver=driver,
        tracer_provider=tp,
        meter_provider=mp,
        logger_provider=lp,
    )


def make_provider_bundles(
    drivers: list[DriverInfo],
    session: SessionInfo,
    endpoint: str,
) -> Dict[str, ProviderBundle]:
    """Build one ProviderBundle per driver, keyed by driver code."""
    return {d.code: make_provider_bundle(d, session, endpoint) for d in drivers}
