# SCL Metrics and Alerting Reference

This file complements `docs/runbook.md` with concrete metric naming and examples.

## Suggested Metric Names

- `scl_resume_attempts_total`
- `scl_resume_accepted_total`
- `scl_resume_rejected_total{reason=...}`
- `scl_resume_partial_total`
- `scl_retransmit_messages_total`
- `scl_retransmit_batch_size`
- `scl_disconnect_events_total{reason=...}`
- `scl_active_sessions`

## Prometheus-Style Alert Examples

```yaml
groups:
  - name: scl-alerts
    rules:
      - alert: SCLResumeFailureRateHigh
        expr: |
          (
            sum(rate(scl_resume_rejected_total[10m]))
            /
            clamp_min(sum(rate(scl_resume_attempts_total[10m])), 1)
          ) > 0.05
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "SCL resume rejection rate > 5%"

      - alert: SCLInvalidTokenSpike
        expr: |
          sum(rate(scl_resume_rejected_total{reason="invalid_token"}[10m]))
          > 2 * sum(rate(scl_resume_rejected_total{reason="invalid_token"}[1h]))
        for: 10m
        labels:
          severity: critical
        annotations:
          summary: "SCL invalid_token rejection spike"

      - alert: SCLPartialRecoveryElevated
        expr: |
          (
            sum(rate(scl_resume_partial_total[15m]))
            /
            clamp_min(sum(rate(scl_resume_accepted_total[15m])), 1)
          ) > 0.01
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "SCL partial recovery rate > 1%"
```

## Logging Fields (recommended)

Include these fields in resume/disconnect logs:

- `session_id` (or hashed/truncated form)
- `resume_accepted`
- `resume_reason` (if rejected)
- `resume_point`
- `partial`
- `oldest_recoverable`
- `disconnect_reason`
- `reconnect_count`

## Notes

- Keep token values out of logs.
- Use cardinality-safe labels for reasons and environments only.
