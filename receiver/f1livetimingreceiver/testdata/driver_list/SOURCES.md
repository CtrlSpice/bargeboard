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
