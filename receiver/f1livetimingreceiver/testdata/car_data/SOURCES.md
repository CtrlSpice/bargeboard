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
