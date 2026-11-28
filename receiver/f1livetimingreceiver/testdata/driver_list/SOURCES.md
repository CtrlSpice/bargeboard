# DriverList Test Data

All DriverList payloads in `driver_list_test.go` and
`driver_registry_test.go` are compact synthetic JSON written for this
repository. They are derived only from the canonical grammar and state-machine
contract in `docs/architecture.md`; they are not copied, transformed, or
re-encoded from Formula 1 Live Timing responses or archives.

Synthetic identifiers use numeric keys and invented acronyms such as `AAA`,
`BBB`, and `ABCD`. Boundary builders generate the 32-entry and 33-entry cases
deterministically. Escaped surrogate cases are deliberate JSON scalar-validity
mutations, not captured source values.

Published DriverList payloads follow the accepted
[Source Evidence and Synthetic Fixtures policy](../../../../docs/architecture.md#source-evidence-and-synthetic-fixtures).
These tests do not establish new source mappings or live availability.

The pure core exercises the approved grammar and state-machine policy. Official
archive references confirming canonical positive decimal entry keys, matching
string `RacingNumber`, and uppercase ASCII `Tla` include
[2024 British Grand Prix race DriverList](https://livetiming.formula1.com/static/2024/2024-07-07_British_Grand_Prix/2024-07-07_Race/DriverList.json)
and
[2025 British Grand Prix race DriverList](https://livetiming.formula1.com/static/2025/2025-07-06_British_Grand_Prix/2025-07-06_Race/DriverList.json).
Tests never fetch those URLs and retain only synthetic fixtures.
