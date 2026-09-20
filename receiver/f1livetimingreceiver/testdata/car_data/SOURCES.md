# Synthetic Normalization Fixture

`normalization_cases.json` contains independently authored synthetic data: a fixed
raw-DEFLATE token encoded as standard base64, a synthetic feed-wrapper timestamp,
and the exact expected plaintext bytes stored as a JSON string. The payload,
identifiers, and timestamps are invented, with no copied archive content.

The token was generated once in memory using Python 3.9.6 and zlib 1.2.12, with
compression level 9 and `wbits=-15` (raw DEFLATE, without a zlib or gzip wrapper):

```python
import base64
import zlib

payload = (
    br'{"synthetic":true,"timestamp":"2032-04-05T06:07:08.009Z",'
    br'"nested":{"values":[17,-2.5,null,{"label":"invented\u0020value",'
    br'"ready":false}]}}'
)
compressor = zlib.compressobj(level=9, wbits=-15)
compressed = compressor.compress(payload) + compressor.flush()
print(base64.b64encode(compressed).decode("ascii"))
```

The plaintext is 138 bytes with no trailing newline; its `\u0020` escape remains
six literal ASCII bytes. The compressed stream is 120 bytes. The Go test reads
the fixed token and compares the complete normalized result against the exact
plaintext, topic, UTC timestamp, and feed source. It does not regenerate the
token with Go's DEFLATE encoder. It also checks preservation of the entire input
and its backing storage, including spare capacity, then clears that storage to
prove the normalized payload is detached.

The wrapper timestamp `2032-04-05T08:07:09.123456789+02:00` normalizes to
`2032-04-05T06:07:09.123456789Z`, independently of the payload's earlier
`timestamp`. Nested objects, arrays, scalars, and the escaped text test opaque
byte preservation. The `CarData.z` topic tests compression suffix removal only;
this fixture establishes no CarData container shapes, channel mappings, active
driver rules, sentinel handling, or scaling. The CarData source adapter remains
**YELLOW** in the canonical architecture.

## Prior Source Observation

The first record of the official
[2025 British Grand Prix race CarData archive](https://livetiming.formula1.com/static/2025/2025-07-06_British_Grand_Prix/2025-07-06_Race/CarData.z.jsonStream)
was retrieved on 2026-09-16. Its JSON string content used standard base64 encoding
of a raw-DEFLATE stream that inflated to JSON. These are the prior source
observations supporting the normalization representation; the synthetic vector
above exercises that contract independently.

The archive's UTF-8 BOM, CRLF records, and relative-time prefix were stream framing,
not part of the compressed token. That archive record was not a complete SignalR
feed invocation and did not establish live wrapper timestamps. No source record
is retained here, and these observations establish no CarData channel semantics
or current live availability. Tests never fetch the reference URL.
