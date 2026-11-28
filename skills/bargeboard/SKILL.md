---
name: bargeboard
description: Discover and ingest Formula 1 sessions, then inspect driver telemetry with odv.
---

# AI race engineer

List meetings and sessions for the current UTC season with
`bargeboard sessions --json`. Use `bargeboard sessions 2025 --json` for a
specific season. Select a completed session with `year/meeting/session`.
Replace spaces in names with hyphens, as in `2025/las-vegas/race`.

`sessions` and `replay` use `~/.config/bargeboard/f1tv-token` for the season
index and archive files. Use `--token-file PATH` to select a different file.
If the default file is missing, both commands try the archive without a token.
On HTTP 401 or 403, refresh the single-line F1 TV subscription token in the
selected file.

Run `bargeboard replay 2025/last/race --once --db ./bargeboard.duckdb` to
save the last completed Race of 2025.
Wait for a successful exit before opening the database. The JSON summary names
the meeting, session and database. `trace_id_sample` is an empty string if
replay produced no driver trace. Without `--once`, Bargeboard runs the viewer
at `http://127.0.0.1:8000` and leaves it open after replay.

Install the CLI from [otel-desktop-viewer](https://github.com/CtrlSpice/otel-desktop-viewer).
After a `--once` replay, start the viewer with `odv --db ./bargeboard.duckdb`
and run queries in a second terminal. Copy `trace_id_sample` from the JSON
summary into `odv trace <trace_id_sample>`. If the sample is empty, use
`odv metrics` or list the SQL tables with `odv query 'show tables'`.

- Follow a driver's `driver.session` span through `stint`, `lap` and `sector`.
  Qualifying adds a `qualifying.phase` parent above the stint. Read `f1.*`
  attributes for driver, session and lap identity.
- Inspect `f1.field.speed` and `f1.field.engine_speed` as delta exponential
  histograms. Each point describes a 10-second window. Use
  `odv export metric <id>` with the ID from `odv metrics` to inspect the
  exemplars' trace and span IDs for the lowest and highest readings.
- Inspect `f1.circuit.corner.*` and `f1.circuit.marshal_sector` on events when
  circuit geometry and driver coordinates were available.
- OpenF1 reports observed Race and Sprint result changes. Jolpica reports
  published classifications and standings. The `replay` command runs neither
  receiver. See the companion receiver configuration in the Bargeboard README.
