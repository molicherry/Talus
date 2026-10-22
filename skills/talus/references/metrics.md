# Monitoring API

Use `X-API-Key` with `metrics:read` and access to the target `server_id` for
`GET /api/v1/servers/{id}/metrics`. Server detail/status requests separately
require `servers:read`.

## Query parameters

| Parameter | Values and defaults |
| --- | --- |
| `from` | RFC3339 timestamp with timezone; default is now minus one hour |
| `to` | RFC3339 timestamp with timezone; default is now |
| `interval` | `auto` (default), `1m`, `5m`, `15m`, `1h`, or `6h` |
| `stat` | `avg` (default), `peak`, or the compatible alias `max` |
| `allow_coarsen` | `true` or `1` permits coarser aggregation for an explicit interval; default false |

The range is `[from,to)`: `to` must be after `from`, and the span must be at most
30 days. `auto` selects an interval that fits the query budget. An explicit
interval is strict unless `allow_coarsen` is enabled. Always read the returned
`actual_interval`; never label a coarsened series with the requested interval.

Example for the last hour, after selecting server ID 1:

```bash
curl --silent --show-error --fail-with-body --get \
  "${TALUS_URL%/}/api/v1/servers/1/metrics" \
  -H "X-API-Key: $TALUS_API_KEY" \
  --data-urlencode 'interval=auto' --data-urlencode 'stat=avg'
```

For a historical range, add `from` and `to` using `--data-urlencode`, especially
when a timestamp includes a positive timezone offset. Use the user's intended
time range rather than copying fixed dates from an example.

## Response

`data` is an object. This is an illustrative response for a range with no samples:

```json
{
  "data": {
    "server_id": 1,
    "from": "2026-10-22T09:00:00Z",
    "to": "2026-10-22T10:00:00Z",
    "requested_interval": "auto",
    "actual_interval": "1m",
    "coarsen_reason": "",
    "stat": "avg",
    "coverage": 0,
    "source": "raw",
    "downsampled": false,
    "points": []
  }
}
```

- Read measurements from `data.points`. `points:[]` means no points were returned.
- `source` is `raw`, `rollup`, or `mixed`. `coarsen_reason` can explain an explicit
  interval's coarsening. It can be empty for `auto`; always use `actual_interval`
  to determine the result's resolution.
- `coverage` is the fraction of requested time buckets containing an observation
  (0–1). It is not a guarantee that every metric in those buckets is available.
- `downsampled` reports explicit interval coarsening or rollup use. A false value
  does not mean the points are unaggregated samples. It is independent of connectivity
  and freshness.
- `stat=max` is echoed as `max`. Both average and sampled-peak columns remain
  in each point; the requested statistic does not rename the columns.

Each point's `time` is its UTC bucket boundary. `sampled_at`, when present, is
the latest actual sample time in that bucket. Use the latter when describing
observation time; the bucket start is not the time of a fresh observation.

| Measurement | Average field | Sampled peak field | Unit |
| --- | --- | --- | --- |
| CPU | `cpu_percent` | `cpu_peak` | Percent |
| Memory | `memory_percent` | `memory_peak` | Percent |
| Disk usage | `disk_percent` | `disk_peak` | Percent |
| Swap | `swap_percent` | `swap_peak` | Percent |
| Load | `load_1`, `load_5`, `load_15` | `load_1_peak`, `load_5_peak`, `load_15_peak` | Load average |
| Network receive | `net_recv_rate` | `net_recv_peak` | Bytes/second |
| Network send | `net_sent_rate` | `net_sent_peak` | Bytes/second |
| Disk read | `disk_read_rate` | `disk_read_peak` | Bytes/second |
| Disk write | `disk_write_rate` | `disk_write_peak` | Bytes/second |

For `stat=peak`/`max`, select the peak fields; for `avg`, select the average
fields. Peaks are maxima of observed samples, not guaranteed continuous-time
maxima. Numeric values may be `null`; preserve gaps instead of converting them
to zero. A non-null zero is an actual zero measurement.

Gauge sums/counts are also available for combining buckets: `cpu_sum`/`cpu_count`,
`memory_percent_sum`/`memory_percent_count`, `disk_percent_sum`/`disk_percent_count`,
`swap_percent_sum`/`swap_percent_count`, and `load1_sum`/`load1_count` (likewise
`load5` and `load15`). Compute a window average as total sum / total valid count,
not as the unweighted mean of bucket averages. For rates, weight the average
by its corresponding valid coverage seconds: `net_recv_coverage`,
`net_sent_coverage`, `disk_read_coverage`, or `disk_write_coverage`. If the total
count/coverage is zero, report the measurement as unavailable.

## Query limits and failures

Queries enforce at most 600 output buckets, a 12,000-sample raw-input budget,
and a two-second query deadline. Longer queries can use stored rollups.

| HTTP status / `error.reason` | Meaning and response |
| --- | --- |
| `400 invalid_query` | Fix invalid timestamps, ordering, range over 30 days, interval, or statistic. The server does not silently shorten the requested range. |
| `422 metrics_query_budget_exceeded` | Read `error.params.recommended_interval`. Retry at that interval or use `allow_coarsen=true` if lower resolution meets the task. Preserve and report the actual resolution. |
| `503 metrics_rollup_not_ready` | Required rollup data is not ready. Retry with backoff or request a smaller range; report any reduced time coverage. |
| `503 metrics_query_timeout` | The shared query deadline expired. Retry with backoff or a smaller query; a client-side timeout increase does not change the server deadline. |

Do not turn a query failure into an empty/zero series or report an unqueried
portion of the requested range as healthy.

## Monitoring status

`GET /api/v1/servers`, `/servers/summary`, and `/servers/{id}` expose a
`monitoring` object when monitoring status is available. Its dimensions are separate:

| Field | Interpretation |
| --- | --- |
| `connection` | `online`, `reconnecting`, `unreachable`, or `unknown`: agent contact/connection state, not an SSH connectivity test |
| `collection` | `healthy`, `partial`, `failed`, or `unknown`: validity of the latest collected sample |
| `data` | `fresh`, `backfilling`, or `stale`: current persisted sample freshness and outstanding replay |
| `deployment`, `deployment_error` | Installation state and diagnostic; `setup_required` with `hub_url_required` means the Hub needs `AGENT_HUB_URL` |
| `last_contact_at`, `last_sample_at`, `last_persisted_at` | Separate contact, sample, and persistence timestamps, when present |
| `errors` | Per-component collection/storage diagnostics, when present |
| `pending_samples`, `dropped_samples`, `version` | Buffered backlog, dropped sample count, and agent version |

Resident agents normally sample every five seconds (`MONITOR_INTERVAL` configures
1–60 seconds). Contact expires after 45 seconds. Data freshness uses three sampling
intervals, with a minimum of 15 seconds. Replayed history can fill gaps without
making a disconnected node online; a recent storage timestamp alone does not prove
the underlying sample is recent. Report missing/stale values explicitly.

The Hub installs/repairs agents over the configured SSH/SFTP route. Nodes report
to the node-reachable HTTPS `AGENT_HUB_URL` over outbound WSS with HTTPS fallback.
Installation requires running systemd and `flock`; non-root SSH accounts need
user lingering. Talus does not automatically elevate via sudo. Deployment
problems and missing Hub configuration need administrator attention, not changes
to the metric query or an assumption that the server is offline.
