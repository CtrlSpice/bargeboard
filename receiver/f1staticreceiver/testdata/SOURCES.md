# Static archive fixtures

The [static archive contract](../../../docs/architecture.md#static-archive-replay-receiver-f1static) describes the two bootstrap files, stream prefixes, compression and UTC anchors. Separate topic files do not record cross-topic callback order.

`archive_test.go` uses an invented meeting, roster, token and publication times. Its loopback server serves these records from memory. The `Position` vector encodes `{"Position":[]}` with Python's `zlib.compressobj(wbits=-15)` and standard base64. The regressing prefix and corrupt streams are synthetic boundary cases, not observations from a race.
