# flywheel metrics

`flywheel stats --metrics [--window 24h|7d|30d] [--json]` computes the factory's lean metrics from the
event log alone ([#583](https://github.com/suzworx/flywheel/issues/583)). This page is the contract:
every number the stats panel, the `:pulse` dashboard and the `:metrics` view show is defined here,
from the ledger, with its unit. The engine is `flywheel.Metrics` in `internal/flywheel/metrics.go`;
it reads nothing but the events and the config, and the same ledger always gives the same report.

## Window and buckets

A window is `since <= ts < until`. `--window 24h` ends now and has 1-hour buckets; `7d` and `30d`
have 1-day buckets. Every series is a list of numbers with one value per bucket, aligned to
`buckets` (each bucket's start time), so a chart draws it directly. An event whose `ts` does not
parse counts nowhere. The text table's trend arrow compares each value with the same metric over
the previous window of equal length: `↑` higher, `↓` lower, `→` equal.

Terms used below:

- **landed unit**: a task whose first `landed` event is in the window.
- **attempt span**: a `dispatched` event to the same task and attempt's `finished` event. A span no
  `finished` closes ends at the task's next `dispatched`, `lost`, `withdrawn` or `landed` event, or
  at the window's end while none came.
- **correction**: an attempt beyond a unit's first; a resume or a correction is attempt `c<n>`.

JSON durations are nanoseconds; a distribution (`Dist`) is `count`, `p50`, `p90`, `max`, `mean`,
the percentiles by nearest rank.

## Flow

| Metric | Definition | Unit |
|---|---|---|
| `throughput` | landed units | units |
| `throughput_series` | landed units per bucket, by the landing's bucket | units per bucket |
| `wip` | tasks whose derived status (as `flywheel state` derives it from the events before the window's end) is `dispatched`, `running`, `finished` or `passed` | units |
| `wip_series` | `wip` at each bucket's end | units |
| `lead_time` | per landed unit: first `planned` to first `landed` | Dist |
| `cycle_time` | per landed unit: first `dispatched` to first `landed` | Dist |
| `queue_time` | per landed unit: first `planned` to first `dispatched` | Dist |
| `touch_time` | per landed unit: the sum of its attempt spans closed by a `finished` event | Dist |
| `flow_efficiency` | mean over landed units with a positive cycle time of touch time / cycle time | ratio 0..1 |

## Quality

| Metric | Definition | Unit |
|---|---|---|
| `landed` | landed units | units |
| `first_pass` | landed units whose only attempt is `r1` and that never got an `inspected` or `reviewed` verdict `correct` or `rework` | units |
| `first_pass_yield` | `first_pass / landed` | ratio |
| `corrections` | over landed units, the distinct attempts beyond the first | attempts |
| `rework_rate` | `corrections / landed` | corrections per landed unit |
| `gates` | per gate id (the gate's 1-based index) and gate command clipped to 60 characters: the window's conclusive `validated` readings (an `rc`, reason neither `inconclusive` nor `host-blocked`), `pass` those with rc 0, `rate = pass / total` | readings, ratio |
| `reviewed` | tasks with an agent `reviewed` event (persona `reviewer` or `reviewer:<dimension>`, an adapter recorded) in the window | units |
| `findings`, `blocking` | `review_finding` events in the window; of those, severity `blocker` or `major` | findings |
| `review_find_rate` | `findings / reviewed` | findings per reviewed unit |
| `blocking_share` | `blocking / findings` | ratio |
| `escapes` | landed units with a `planned` event after their landing (at any time) | units |

## Reliability

| Metric | Definition | Unit |
|---|---|---|
| `andons` | andons in the window by kind: every `signal` event (kind = its signal), and every `finished` event with reason `stalled`, `silent`, `capped` or `rate-limited` that no `signal` of the same name, task and attempt already records (kind = the reason) | andons |
| `andon_total`, `andon_series` | the sum of `andons`; andons per bucket | andons |
| `cleared` | andons on a task that a later event of the task cleared: `landed`, `withdrawn`, `dismissed`, or an `inspected` or hand-recorded `reviewed` verdict `pass` | andons |
| `mttr` | per cleared andon: the andon to its first clearing event | Dist |
| `frozen.total` | the time inside the window the factory was suspended: from a `suspended` event to the first `unsuspended` event or its `until`, whichever comes first; a later `suspended` event while frozen moves the thaw time; one still open runs to the window's end | duration |
| `frozen.manual`, `frozen.auto` | the same split by who froze it; the `suspended` event carries no automatic flag yet, so every interval is manual and `auto` is 0 | duration |
| `paused` | per model, sorted: the time inside the window a rate limit paused it. At every `finished` event on the model, the rate-limit pause rule `flywheel run` applies (a `reset_at` still ahead, or `limit_utilization` at least `limits.rate_limit_pause_at` with `limit_reset_at` ahead) decides whether it is paused; the pause lasts until its reset or the model's next `finished` event | duration |

## Cost

| Metric | Definition | Unit |
|---|---|---|
| `spend` | the `cost` of the window's `finished` and `reviewed` events | USD |
| `spend_series` | `spend` per bucket | USD per bucket |
| `units` | tasks with a `finished` event in the window | units |
| `cost_per_unit` | `spend / units` | USD |
| `cost_per_landed` | `spend / landed` | USD |
| `tokens`, `steps` | the window's `finished` events' tokens and steps | tokens, steps |
| `tokens_per_step` | (input + output + reasoning tokens) / steps | tokens |
| `by_model` | the `flywheel stats --by model` scoreboard computed over the window's events only | per model |

## Capacity

| Metric | Definition | Unit |
|---|---|---|
| `workers[].busy_seconds` | the attempt spans inside the window of the worker: the `dispatched` event's worker, else the configured worker on its model, else the default worker | seconds |
| `workers[].utilization` | `busy_seconds / (max_parallel × window seconds)`, `max_parallel` at least 1 | ratio |
| `utilization` | every worker's busy seconds / every worker's capacity seconds | ratio |
| `idle_share` | `1 - utilization`, never below 0 | ratio |

Ratios and USD are rounded to 4 places, `tokens_per_step` to 2, `busy_seconds` to 3; a ratio whose
denominator is 0 is 0.
