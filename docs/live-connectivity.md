# Private Live Timing Connectivity Check

This optional check runs the production Bargeboard Collector on your machine
using your existing F1 TV token file. It answers whether the current connection,
subscription, and normalization path works with the source. Public CI covers
implementation behavior with synthetic inputs and local servers instead.

**Racing OTLP export is not implemented.** An empty desktop viewer is expected
today, even when this input check succeeds. The
[canonical boundary](architecture.md#local-runtime-and-verification-boundaries)
keeps F1 credentials in Bargeboard and the viewer a generic OTLP destination.

## Prepare

1. Use your own existing `subscriptionToken` in the local file described in the
   [README](../README.md#f1-live-timing). Keep the contents out of YAML, command
   arguments, shell history, screenshots, logs, issues, and GitHub secrets. On
   POSIX systems, make the existing file owner-only:

   ```sh
   chmod 600 "$HOME/.config/bargeboard/f1tv-token"
   ```

   This command changes permissions; it does not create or acquire a token.
   Windows users should follow the README's HOME setup and restrict file access
   to their account. The reader currently does not enforce file permissions.

2. Run this input-only check with the viewer stopped. The shipped configuration
   needs `localhost:4317`, `localhost:4318`, and `127.0.0.1:8888` free. If another
   service owns those ports, stop the check and resolve the local configuration
   conflict; a bind failure says nothing about F1 access. A user-controlled
   configuration may instead omit Bargeboard's incoming OTLP receiver and use
   a free loopback operational-metrics port.

3. From the repository root, validate the configuration:

   ```sh
   make validate
   ```

   This checks configuration shape, not token-file readability, token validity,
   or network access. For a different local configuration, supply the same
   `CONFIG` path to both `make validate` and `make run`.

4. Choose a short observation window before starting, for example 90 seconds.
   This is a manual check: Bargeboard has no overall probe deadline and continues
   until you stop it or startup fails. Startup connection errors return without
   a startup retry loop; later retriable input failures use the production retry
   policy.

## Run and Observe

Start the real runtime in the foreground:

```sh
make run
```

Keep its terminal visible. The first-data notice is:

> First Live Timing updates observed with validated subscription; no F1 race export is implemented

This means subscription acceptance and at least one normalized update occurred
on the same connection. It does not imply complete race coverage, coherent
racing semantics, or downstream export.

In another terminal, you can inspect the local operational metrics without
saving a capture:

```sh
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:8888/metrics
```

Use the configured loopback port if you selected a different configuration.
The shipped configuration exposes the following names:

| Observation | What it establishes |
|---|---|
| `otelcol_f1livetiming_connection_active=1` | Setup completed through credential-bearing negotiation, WebSocket/SignalR handshake, and the Subscribe write. It does not establish subscription acceptance. |
| `otelcol_f1livetiming_subscription_active=1` | The current subscription completion and its snapshot passed normalization. |
| `otelcol_f1livetiming_normalized_updates` increases | Update envelopes were accepted in a complete normalized batch. A subscription snapshot can supply these updates; this does not prove continuing feed traffic or any particular topic. |
| `otelcol_f1livetiming_last_update_age` | Time since local acceptance of the last nonempty batch; absent before the first one. It is not source freshness. |
| First-data or recovery notice | The current connection met the combined subscription-plus-update condition. |

The preflight OPTIONS request is unauthenticated. Its affinity-cookie acceptance
alone cannot validate your token. Likewise, counters survive reconnects: an old
nonzero update total plus a new accepted subscription cannot prove that updates
arrived on that new connection. Use the first-data/recovery notice for that claim.

An accepted empty subscription can remain waiting while hub pings continue.
No updates within your window is not by itself a parsing failure. Complete
transport silence can still trigger the existing receive timeout. A 401/403
setup rejection means check your token and access; it does not identify the
precise upstream account, entitlement, or device-policy cause.

## Stop and Interpret

Press **Ctrl-C** at the end of your window, or after a terminal input failure.
Wait for the input summary and actual Collector exit. A terminal F1 failure can
stop input while leaving the Collector process running, so process survival is
not a success criterion. Initial setup failure may return before the runtime
summary lifecycle begins.

The final notice says:

> Live Timing input run ended; interruption summary (not an export summary)

Record only the highest stage you observed, relevant bounded stage/status
failures, operational totals, any unresolved outage, and whether shutdown
completed. Do not hide a later failure behind an earlier successful milestone.
Connection and subscription gauges clear on stop; their final zero values do not
erase earlier observations. If shutdown fails to complete, record that outcome
rather than treating the check as passed.

Keep tokens, cookies, source records, and any local diagnostic output private.
If reporting a defect publicly, share a sanitized stage/status summary and an
independently authored synthetic reproducer. This procedure does not create a
raw recording or upload evidence automatically.

The client does not acquire/refresh tokens or register an F1 TV device. A
successful check establishes only the technical outcomes above. It does not
establish permission under the provider's
[subscription terms](https://account.formula1.com/#/en/subscription-terms), or
whether/how the provider counts the connection against device limits.
