# Negotiation fixture provenance

`responses.json` transcribes the four JSON examples (version 1, version 0,
redirect, error) from ASP.NET Core **v8.0.0**
[`TransportProtocols.md`](https://github.com/dotnet/aspnetcore/blob/v8.0.0/src/SignalR/docs/specs/TransportProtocols.md),
under `POST [endpoint-base]/negotiate`. Only whitespace and the enclosing named
test-case array differ. The IDs, URL, and access-token text are public example
values, not credentials or captured F1 wire evidence.

The same tag's
[`NegotiateProtocol.cs`](https://github.com/dotnet/aspnetcore/blob/v8.0.0/src/SignalR/common/Http.Connections.Common/src/NegotiateProtocol.cs)
supplies implementation evidence: canonical property constants, exact decoded
name matching (`ValueTextEquals`), version default zero, fresh capability objects,
and opaque skipping of unknown members. The document supports ignoring extra
properties and selecting the version-dependent connection identity.

These sources support the names and response structure, **not a protocol mandate
to reject duplicates or every null**. The reference reader assigns repeated
properties; its writer can emit a null transport name. Bargeboard's approved
N-CONTROL policy deliberately rejects duplicate known members, noncanonical
case-fold aliases, present null known members, and null array entries to prevent
ambiguous controls, rejection erasure, and inherited capabilities. It retains its
own empty-string/incomplete-capability domain checks and unsupported redirects.

`negotiation_controls_test.go` labels derived boundary probes as synthetic:
omitted version, alternate identities, escaped names, duplicate/case/null/type
mutations, rejection erasure, capability inheritance, Unicode, opaque extensions,
depth/byte limits, destination reuse, and in-memory HTTP setup failures. They
establish local acceptance and failure policy, not observed service behavior.
