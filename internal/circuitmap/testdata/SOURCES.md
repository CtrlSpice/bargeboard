# Circuit geometry fixtures

The source schema is MultiViewer's `GET https://api.multiviewer.app/api/v1/circuits/{circuitKey}/{year}`.
Corners and marshal sectors carry a `number` and a `trackPosition` with `x` and `y` in decimetres. A corner can also have a `letter`. Lookup uses these positions before display rotation.

`circuit.json` and the inline test cases use invented coordinates and identifiers. The HTTP tests serve these fixtures locally. None of the fixtures contain captured F1 or MultiViewer data.
