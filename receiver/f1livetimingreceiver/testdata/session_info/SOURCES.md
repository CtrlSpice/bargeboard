# SessionInfo Fixture Sources

`classification_cases.json` contains pinned records retrieved from the public F1
Live Timing static archive on 2026-09-04. Each case records its direct source URL.
Tests never fetch these URLs.

The payloads preserve the source fields and representative ignored metadata. The
`2021_abu_dhabi_practice_1_stream` payload removes only the archive record prefix
`00:00:00.000`; all other payloads come from `SessionInfo.json` objects.

The two Abu Dhabi Practice 1 cases intentionally preserve the source correction
from route key `6594` in the stream to `7165` in the later snapshot while the
logical identity and schedule remain unchanged.
