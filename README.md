# bargeboard

> *Ceci n'est pas un déflecteur latéral.*

A custom OpenTelemetry Collector distribution for Formula 1 telemetry. Designed as a demo companion to [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer).

## Implementation

The Go Collector distribution accepts OTLP traces, metrics, and logs, batches them, and stores them through the embedded viewer's `desktop` exporter. The F1 Live Timing receiver connects to the live feed, validates updates, and reports input activity and interruptions. It projects driver timing spans, racing metrics, and event logs for synchronized sessions. An OpenF1 receiver does not exist yet.

The canonical design is the evolving [Bargeboard architecture](docs/architecture.md). It records accepted decisions, source limitations, pending candidates, implementation seams, and the required checks for future human and agent contributors.

## Quick start

Development requires Go 1.27.0 or newer and a native C/C++ toolchain with CGO
enabled for DuckDB. CI authenticates and verifies Go 1.27.4. Native CGO release
archives use the same pinned version.
Before `make run`, prepare your [F1 token file](#f1-live-timing).

The commands below require GNU Make and a POSIX shell, such as Git Bash on
Windows. Use Xcode Command Line Tools on macOS, GCC/G++ on Linux, or MinGW-w64
GCC/G++ on Windows. `go env CGO_ENABLED CC CXX` reports whether CGO is enabled
and which compilers Go uses; `CGO_ENABLED` must be `1`.

```bash
make check
make run
```

The shipped `config.yaml` enables F1 Live Timing and listens for OTLP/gRPC on
`localhost:4317` and OTLP/HTTP on `localhost:4318`. Run `make components` to
inspect the components compiled into the distribution.

Open [the embedded viewer](http://127.0.0.1:8000) after startup. The `duckdb`
extension stores telemetry in `./bargeboard.duckdb`, relative to the process
working directory. Data survives shutdown and restart. `db_max_size: "0"`
disables automatic pruning.
The viewer displays racing telemetry and data from other OTLP clients.
The incoming OTLP ports and the viewer belong to the same Bargeboard process.

`make check` uses synthetic F1 inputs and local test servers. `make run` starts
the real client using the user's token file. For an optional private check of
that connection, follow [Live Timing connectivity](https://github.com/CtrlSpice/bargeboard/blob/main/docs/live-connectivity.md).

## Releases

Native CGO release archives target Linux on amd64 and arm64, macOS on arm64,
and Windows on amd64.
Each `bargeboard` archive must include the binary,
`config.yaml`, this README, and the Apache 2.0 license. The release must also include
SHA-256 checksums and an SPDX SBOM for every archive.

When a version is available on the [release page](https://github.com/CtrlSpice/bargeboard/releases),
download and verify its assets before running the binary. Replace `v1.2.3` with
that version:

```bash
tag=v1.2.3
gh release download "$tag" --repo CtrlSpice/bargeboard
gh release verify "$tag" --repo CtrlSpice/bargeboard
for asset in checksums.txt bargeboard_*; do
  gh release verify-asset "$tag" "$asset" --repo CtrlSpice/bargeboard
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum --check checksums.txt
else
  shasum -a 256 --check checksums.txt
fi
```

Extract the archive for your platform, then run its binary from the extracted
directory with `--config config.yaml`. Prepare the [F1 token file](#f1-live-timing)
first. For example, the Linux amd64 archive for `v1.2.3` would use:

```bash
tar -xzf bargeboard_1.2.3_linux_amd64.tar.gz
cd bargeboard_1.2.3_linux_amd64
./bargeboard --config config.yaml
```

On Windows, extract the ZIP and run `.\bargeboard.exe --config config.yaml`
from its directory.

The release workflow runs trusted code from `main` after a maintainer dispatches
a signed annotated `v0` or `v1` SemVer tag such as `v1.2.3`. Build metadata is
not accepted because Go cannot represent it in this module's embedded version.
The tag must point to the current `main` commit, that commit must have passed the
protected `check` workflow, and the configured release controls must still match
repository policy. Native jobs build each binary twice using separate source and
Go cache directories. A required reviewer approves the protected `release`
environment before GoReleaser packages both sets without uploading them. The
workflow compares and verifies every archive, SBOM, and checksum manifest before
it uploads, reverifies, and publishes the draft. GitHub immutability then locks the tag
and assets. At publication, the workflow also requires the tag as the title,
empty notes, and the expected prerelease state. It sends `make_latest: "false"`
to opt out of promoting the release to latest. GitHub still allows maintainers
to edit those display fields later. CI authenticates the official Go
1.27.4, Syft, GoReleaser Pro, and actionlint archives against SHA-256 digests
pinned in the repository before extracting or executing them. Tags are limited
to 131 ASCII characters so every wrapped archive path has one canonical USTAR
representation.

Each archive includes a deterministic `THIRD_PARTY_NOTICES` inventory of the Go
standard library and dependency modules selected across the four targets. The
generator records legal documents by path and SHA-256 and includes each distinct
normalized text once. It rejects forbidden license families and restrictive
terms in license files. Notice and attribution files are preserved as text.

Archives include the pinned MPL-2.0 source for `go-version`, `golang-lru`, and
the Public Suffix List compiled into `golang.org/x/net/publicsuffix`, plus the
list's license. The embedded viewer serves Madoromi's bundled MPL-2.0 source at
`/licenses/madoromi/`.

The final verified read of `main` immediately before publication is the release
decision point. A later branch update does not invalidate that decision. A
failed run before publication can leave an unpublished draft that maintainers
must inspect and remove before retrying. If a publication request fails, the
workflow accepts only a positively reconciled valid immutable release and
removes a positively reconciled invalid public release. Unknown outcomes and
unpublished drafts are preserved for manual reconciliation because deleting an
immutable release permanently prevents reuse of its tag name.

Maintainers can validate the external controls with the release control token
before dispatching a tag:

```bash
GITHUB_REPOSITORY=CtrlSpice/bargeboard bash scripts/verify-release-controls.sh
```

After creating the signed tag at current `main`, dispatch the workflow through
the repository API so GitHub executes the workflow definition from protected
`main`, not from the tagged commit:

```bash
tag=v1.2.3
gh api --method POST repos/CtrlSpice/bargeboard/dispatches \
  --raw-field event_type=release \
  --field "client_payload[tag]=$tag"
```

The protected environment supplies `GORELEASER_KEY` and a repository-scoped
`RELEASE_CONTROL_TOKEN`. GitHub only discloses ruleset bypass actors to callers
with ruleset write access, so that token needs repository Administration write
permission even though the workflow uses it only for `GET` requests. Keep both
secrets in the `release` environment, not at repository scope.
The environment deployment policy must permit only the `main` branch; the
requested release tag is validated as data inside that trusted workflow.

## F1 Live Timing

Bargeboard owns the F1 credentials and connection. Its local Collector pipelines
deliver telemetry to the embedded `otel-desktop-viewer v0.8.1` exporter and
DuckDB store. Viewer components receive telemetry only and have no F1 credential
handling. The incoming OTLP ports also accept telemetry from other local clients.

The Collector reads each user's own F1 TV `subscriptionToken` from
`$HOME/.config/bargeboard/f1tv-token`. The repository configuration contains
only that file reference, never a token. Create the file with owner-only
permissions before running the Collector. Store the token itself as plain text,
not a JSON response or an `Authorization` header. Bargeboard does not acquire or
refresh tokens. After storing the token:

```bash
chmod 600 "$HOME/.config/bargeboard/f1tv-token"
make run
```

The receiver sends a SignalR keepalive after 15 seconds of outbound inactivity.
It reconnects after 30 seconds of server wait without a complete accepted hub
record, or without the initial subscription completion. Incoming pings keep the
connection active but do not satisfy subscription completion. Local processing
time is excluded from both server-wait budgets.

### Watching Live Timing input

Keep the Collector in the foreground to see its notices on stderr. **Press
Ctrl-C to stop the Collector** and shut down gracefully. Retries have no attempt
limit: exponential backoff grows from one second to 30 seconds. A valid
`Retry-After` response header can extend a wait beyond 30 seconds; the countdown
and actual wait share one deadline, and Ctrl-C still cancels the wait.

During reconnect, network failures and HTTP 408, 429, 500, 502, 503, and 504 are
retried. A WebSocket upgrade 404 also retries with fresh preflight and negotiation;
a negotiation 404 stops input. HTTP 401/403, redirects, and all other unexpected
statuses stop input with a terminal error identifying the setup stage and status.
For 401/403, check your F1 TV token and access. Preflight remains cookie-first:
a nonempty `AWSALBCORS` cookie is accepted even on 405; a successful response
without that cookie is invalid source protocol. Initial connection errors still
fail synchronous startup immediately, without a startup retry loop.

Startup reports a recoverable not-receiving status until the validated
subscription and first normalized updates arrive. Waiting alone does not count
as an outage.

When the receiver detects an interruption, it warns immediately that updates
may be missing. Progress notices include the actual reconnect attempt count,
run elapsed time, current and total outage seconds, and seconds until the next
scheduled attempt. A new connection alone does not mean input has recovered:
the receiver waits for both
a validated subscription completion and at least one normalized update on that
connection. It then reports:

> Live Timing updates resumed; missed updates may be unrecoverable

The first input activity has its own notice, even if it closes an outage before
any input was ready. A recovery notice reports the duration of the gap that just
ended. Waiting for initial updates, unresolved outages, and a stopped input are
reported every 30 process seconds.
Pings and empty subscription snapshots do not count as updates. An idle feed
does not create an additional failure or change the retry policy.

Invalid source data, a terminal HTTP setup failure, or a server close that
disallows reconnect stops the F1 input and produces an error; the Collector can
still run. The input run's final
summary retains outage, recovery, reconnect-attempt, normalized-update, and
consumer-failure totals, plus whether an outage remains unresolved. It also
retains total outage duration, including any open gap as of that summary. These are
input observations, not evidence that any F1 racing signals were exported.

The shipped configuration exposes Collector internal metrics at
[`http://127.0.0.1:8888/metrics`](http://127.0.0.1:8888/metrics):

```bash
curl http://127.0.0.1:8888/metrics
```

The `otelcol_f1livetiming_` metrics describe connection and subscription state,
outages, retries, recoveries, normalized input envelopes, and consumer failures.
`last_update_age` is seconds since the last locally accepted nonempty batch; it
is absent before that first batch and does not measure source freshness.
Input activity alone does not confirm racing export. See the [operational contract](docs/architecture.md#live-timing-operational-visibility)
for names, units, and lifecycle semantics. Setting
`service.telemetry.metrics.level` to `none` disables these metrics; terminal
notices and per-run summary totals remain available.

The packaged Windows configuration uses the same `HOME`-relative path.
PowerShell does not normally export its `$HOME` value as an environment
variable, so set it for the Collector process and create the token file in the
same profile before starting `bargeboard.exe`:

```powershell
$tokenDirectory = Join-Path $HOME ".config\bargeboard"
New-Item -ItemType Directory -Force $tokenDirectory | Out-Null
notepad (Join-Path $tokenDirectory "f1tv-token")
$env:HOME = $HOME
.\bargeboard.exe --config config.yaml
```

The client consumes an existing token; it does not implement account login,
token refresh, or explicit device registration. A successful connection does not
establish provider permission for third-party clients or device-limit treatment.

## Test data

Public F1 payload fixtures are independently authored synthetic inputs. Source
references and observations establish the evidence for source-dependent rules;
synthetic cases exercise those rules without publishing captured race records.
The provenance files under `receiver/f1livetimingreceiver/testdata/` distinguish
those roles. Public CI uses invented credentials and local HTTP/WebSocket servers
and must not contact F1 services or receive F1 account credentials.

## Security

The token file should be readable only by its owner. Never put a token directly
in YAML, shell history, logs, issues, or commits. Keep release credentials in the
protected `release` environment, not at repository scope.

## Acknowledgements

- [OpenF1](https://openf1.org/) for public historical F1 timing and telemetry
  data that informs the accepted future Go source architecture.
- [@anthropic-ai/claude-code](https://docs.claude.com/claude-code) for being the
  world's most patient pair-programmer through this project.

## License

Licensed under the [Apache License 2.0](LICENSE).
