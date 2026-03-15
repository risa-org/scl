# SCL Production Runbook (Template)

Use this as a copy/paste template for each service that embeds SCL.

## 0. Service Metadata

- Service name:
- Team owner:
- On-call rotation:
- Runtime/environment (k8s/VM):
- SCL version/commit:
- Store backend (`memory`/`file`/external):
- Session policy defaults (`Ephemeral`/`Interactive`/`Durable`):

## 1. SLO and Error Budget

- Resume success SLO (rolling 30d):
  - target: `>= 99.5%`
  - formula: `accepted_resumes / total_resume_attempts`
- Reconnect recovery latency SLO (p95):
  - target: `< 2s`
- Partial recovery rate objective:
  - target: `< 0.5%` of accepted resumes

## 2. Required Dashboards

Create dashboards with these panes:

1. **Resume outcomes**
   - total attempts
   - accepted
   - rejected by reason (`session_not_found`, `invalid_token`, `stale_request`, etc.)
2. **Recovery quality**
   - retransmit count distribution
   - partial recovery rate
   - oldest recoverable sequence trends
3. **Transport health**
   - disconnect reasons (`network_error`, `closed_clean`, `timeout`)
4. **Load and safety**
   - active session count
   - sequencer pending depth (`PendingCount`) percentile
   - payload size percentile and max

## 3. Alert Rules (Starter Set)

- **A1: Resume failure spike**
  - condition: rejection rate > 5% for 10 minutes
  - severity: high
  - first checks: token config, store availability, stale request burst

- **A2: Stale request anomaly**
  - condition: `stale_request` rate > baseline + 3σ for 15 minutes
  - severity: medium
  - first checks: client clock drift, retry loops, replay attempts

- **A3: Partial recovery elevated**
  - condition: `partial=true` > 1% for 15 minutes
  - severity: medium/high
  - first checks: missing app-level ACK integration, undersized buffer, store lag

- **A4: Invalid token surge**
  - condition: `invalid_token` > 2x baseline for 10 minutes
  - severity: high
  - first checks: key rotation mismatch, leaked/stale tokens, abuse traffic

## 4. Incident Triage Workflow

1. Identify which alert fired and affected environments.
2. Confirm whether issue is broad (all regions) or isolated.
3. Check resume reject reasons breakdown.
4. Check transport disconnect reason changes.
5. Check store health (latency/errors/corruption signs).
6. Check recent config changes (`maxResumeAge`, token secret, policy TTL).
7. Apply mitigation and monitor burn-down.

## 5. Common Incidents and Playbooks

### Incident: sudden `invalid_token` spike

- Likely causes:
  - mismatched token secret after rollout
  - partial key rotation
  - malicious replay/forgery attempts
- Immediate actions:
  1. verify secret source consistency across replicas
  2. verify deployment version skew
  3. rate-limit abusive source IPs at edge
- Rollback criteria:
  - if mismatch introduced by latest deploy, rollback immediately

### Incident: high `partial` recovery

- Likely causes:
  - ACK path not wired (`seq.Ack(...)` not called)
  - retransmit buffer too small for traffic profile
- Immediate actions:
  1. verify ACK telemetry is flowing
  2. inspect retransmit list sizes and gap patterns
  3. temporarily reduce reconnect pressure with backoff guidance
- Permanent fix:
  - integrate ACK handling, size buffer for worst-case reconnect window

### Incident: stale request rejections after client update

- Likely causes:
  - client clock skew
  - stale cached resume payload reused
- Immediate actions:
  1. verify client timestamp source
  2. increase `maxResumeAge` temporarily if safe
  3. ship client hotfix for timestamp generation/retry semantics

## 6. Change Management Checklist

Before deploying SCL-related changes:

- [ ] `go test ./...` and `go test -race ./...` pass
- [ ] canary tested with reconnect storm simulation
- [ ] dashboards and alerts verified in staging
- [ ] rollback plan prepared and tested
- [ ] key/token changes coordinated across all instances

## 7. Recovery and Rollback

- Safe rollback trigger examples:
  - invalid_token or stale_request rejections sustained above thresholds
  - reconnect latency SLO violation with rising error budget burn
- Rollback sequence:
  1. pause rollout
  2. restore last known good image/config
  3. verify resume acceptance and transport health return to baseline
  4. document incident timeline and root cause

## 8. Post-Incident Review Template

- What happened?
- Customer impact window and magnitude
- Detection quality (which alert, when)
- Root cause
- What mitigated fastest
- Preventative actions (owner + ETA)
