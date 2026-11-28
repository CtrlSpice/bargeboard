# Synthetic SessionInfo Fixtures

`classification_cases.json` contains 20 independently authored synthetic
descriptors. Each has `synthetic: true`, which the tests validate. The payloads
were written from the accepted [Session Coverage](../../../../docs/architecture.md#session-coverage),
[descriptor grammar](../../../../docs/architecture.md#descriptor-grammar), and
[Unicode rules](../../../../docs/architecture.md#u2-sessioninfo-isolation-and-issue-results).
They are not captured sessions or anonymized archive records.

Meeting names, incidental identifiers, schedules, and ignored metadata are
invented. Exact field names and classification vocabulary remain literal because
they are parser inputs: `Pre-Season Test`, `Pre-Season Testing`, the ` Grand Prix`
suffix, and the accepted `Type`/`Name` combinations. The years preserve the
accepted classification eras. In `2020_special_practice`, `Meeting.Key=1057` is
also required production grammar: only season 2020 with that key maps the
unnumbered `Practice` name to `practice_1`. Replacing this constant would remove
coverage of the accepted exception. Its other identifiers and schedule are
invented.

Most descriptors contain only the identity, route, and schedule fields. The
`2021_example_practice_1_initial` case adds invented meeting/circuit metadata,
contradictory `Number` and `Path` values, inert status fields, and a `Future`
object to verify that metadata cannot override descriptor authority. The initial and
corrected Practice 1 cases use distinct routes `101` and `102` with identical
logical identity and schedule. Compact inline descriptors and their mutations in
the SessionInfo parser, Unicode, reducer, batch, and identity-gate tests also use
synthetic values, with the same `101` to `102` route correction.

The independent expected-result table in `session_info_test.go` specifies every
identity, route, UTC schedule, and offset. Tests compare the complete parse
result, including availability and issues, and preserve the input bytes. Expected
values are not computed by the production parser or from the fixture fields.

| Synthetic cases | Accepted behavior retained |
|---|---|
| `2021_preseason_practice_1`, `2022_preseason_practice_2`, `2022_preseason_practice_3` | First testing-name era, both seasons, days 1–3. |
| `2023_preseason_practice_1`, `2024_preseason_practice_2`, `2024_preseason_practice_3` | Middle testing-name era, both seasons, days 1–3. |
| `2025_preseason_day_1`, `2026_preseason_day_2`, `2026_preseason_day_3` | Day-name era, both seasons, days 1–3. |
| `2021_example_practice_1_initial`, `2021_example_practice_1_corrected` | Practice 1 and same-identity/same-schedule route correction. |
| `2021_example_practice_2`, `2021_example_practice_3` | Practice 2 and Practice 3. |
| `2020_special_practice` | The exact 2020 unnumbered-practice exception. |
| `2021_example_qualifying` | Ordinary qualifying. |
| `2023_example_sprint_shootout`, `2024_example_sprint_qualifying` | Both qualifying-like sprint names. |
| `2021_example_sprint_qualifying`, `2023_example_sprint` | The 2021 race-like sprint name and ordinary sprint. |
| `2021_example_race` | Ordinary race. |

## Prior Observations and Research References

The prior archive observations informed the already accepted rules; their scope
and limitations remain recorded in [Live Timing Session Identity](../../../../docs/architecture.md#live-timing-session-identity).
The synthetic fixtures exercise those rules but are not new evidence of what F1
sent, when a session ran, or which source fields were present.

These official archive URLs are non-payload research references retained from the
earlier source attribution, whose records were retrieved on 2026-09-04. Tests
never fetch them, and fixture maintenance does not automatically acquire source
payloads.

- Testing-name eras: [2021](https://livetiming.formula1.com/static/2021/2021-03-14_Pre-Season_Test/2021-03-12_Practice_1/SessionInfo.json), [2022](https://livetiming.formula1.com/static/2022/2022-03-12_Pre-Season_Test/2022-03-11_Practice_2/SessionInfo.json), [2023](https://livetiming.formula1.com/static/2023/2023-02-25_Pre-Season_Testing/2023-02-23_Practice_1/SessionInfo.json), [2024](https://livetiming.formula1.com/static/2024/2024-02-23_Pre-Season_Testing/2024-02-22_Practice_2/SessionInfo.json), [2025](https://livetiming.formula1.com/static/2025/2025-02-28_Pre-Season_Testing/2025-02-26_Day_1/SessionInfo.json), [2026](https://livetiming.formula1.com/static/2026/2026-02-13_Pre-Season_Testing/2026-02-12_Day_2/SessionInfo.json).
- Third testing days: [2022 Practice 3](https://livetiming.formula1.com/static/2022/2022-03-12_Pre-Season_Test/2022-03-12_Practice_3/SessionInfo.json), [2024 Practice 3](https://livetiming.formula1.com/static/2024/2024-02-23_Pre-Season_Testing/2024-02-23_Practice_3/SessionInfo.json), [2026 Day 3](https://livetiming.formula1.com/static/2026/2026-02-13_Pre-Season_Testing/2026-02-13_Day_3/SessionInfo.json).
- The original same-session route correction: [Practice 1 stream](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-10_Practice_1/SessionInfo.jsonStream) and [snapshot](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-10_Practice_1/SessionInfo.json).
- Other ordinary sessions: [Practice 2](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-10_Practice_2/SessionInfo.json), [Practice 3](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-11_Practice_3/SessionInfo.json), [Qualifying](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-11_Qualifying/SessionInfo.json), [Race](https://livetiming.formula1.com/static/2021/2021-12-12_Abu_Dhabi_Grand_Prix/2021-12-12_Race/SessionInfo.json).
- The accepted exception: [2020 unnumbered practice](https://livetiming.formula1.com/static/2020/2020-11-01_Emilia_Romagna_Grand_Prix/2020-10-31_Practice/SessionInfo.json).
- Sprint names: [2021 race-like Sprint Qualifying](https://livetiming.formula1.com/static/2021/2021-07-18_British_Grand_Prix/2021-07-17_Sprint_Qualifying/SessionInfo.json), [2023 Sprint Shootout](https://livetiming.formula1.com/static/2023/2023-04-30_Azerbaijan_Grand_Prix/2023-04-29_Sprint_Shootout/SessionInfo.json), [2024 qualifying-like Sprint Qualifying](https://livetiming.formula1.com/static/2024/2024-06-30_Austrian_Grand_Prix/2024-06-28_Sprint_Qualifying/SessionInfo.json), [2023 Sprint](https://livetiming.formula1.com/static/2023/2023-04-30_Azerbaijan_Grand_Prix/2023-04-29_Sprint/SessionInfo.json).
