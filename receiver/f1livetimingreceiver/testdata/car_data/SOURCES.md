# CarData Normalization Fixture Sources

`normalization_cases.json` contains the first record from the public 2025 British
Grand Prix race `CarData.z` static archive, retrieved on 2026-09-16. The fixture
records its direct source URL, archive-relative prefix `00:01:50.190`, and
compressed token content. Tests never fetch the URL.

The source stream starts with a UTF-8 BOM and terminates records with CRLF. The
fixture discards that stream framing, records the archive prefix separately, and
stores the JSON string without its outer quotes; it does not alter the compressed
bytes. The unquoted compressed content has SHA-256
`b21a290f4ffc24800f470fda9a0e7fefcd0a3a33e4bd08974f690aac26340c73`.
The test restores the quotes before normalization. `inflated_payload` is the exact
2,380-byte raw-DEFLATE result, with SHA-256
`3e2dcbdac301ca7047c6064f4bc8ac0a307e36e0859f3c0c373ae755dbc5c5eb`.

The static archive record is not a complete SignalR feed invocation. Its
`synthetic_feed_timestamp` copies the first inflated entry's `Utc` solely to test
feed timestamp normalization and source propagation. It is not captured wrapper
metadata. This normalization fixture does not establish CarData channel meanings,
active-driver rules, sentinel handling, or scaling.
