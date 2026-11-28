# OpenF1 result fixtures

[OpenF1](https://openf1.org/) documents the `/sessions`, `/drivers`, and
`/session_result` endpoints. The [Bargeboard architecture](../../../docs/architecture.md#openf1-race-like-result-observations)
records observations from 77 completed, non-cancelled Race and Sprint sessions
from 2024 through 2026-09-04 (1,572 result rows). These observations include
classified and zero-lap DNFs, DNS with zero laps, DSQ with null laps, and
positive-lap rows with no position or status flag. OpenF1 reports the current
snapshot without a publication time or revision number.

The tests build their JSON fixtures in `../parse_test.go` using an invented
2042 session. All identities, corrections, malformed responses, HTTP failures,
and observation times in the tests are synthetic. The tests use a loopback
HTTP server and in-memory transports.
