# SCL Production Deployment Guide

This document is the practical checklist for running SCL in real systems.

## 1. Baseline Reliability

- Run full tests in CI:
  - `go test ./...`
  - `go test -race ./...`
- Pin Go version in CI and local development (`go.mod` + workflow).
- Validate reconnect behavior under your expected disconnect patterns.

## 2. Operational Controls (OCS)

### Operations

Track these metrics at minimum:

- Resume attempts (`total`)
- Resume accepted vs rejected (`by reason`)
- Partial recoveries (`partial=true`)
- Retransmit list sizes
- Sequencer pending depth (`PendingCount`)

Recommended alerts:

- Sharp increase in `invalid_token` (possible abuse or key mismatch)
- Sharp increase in `session_not_found` (store consistency issues)
- Sharp increase in `stale_request` (clock/replay problems)

### Controls

- Configure request staleness window:

```go
handler := handshake.NewHandler(store, issuer)
handler.SetMaxResumeAge(90 * time.Second)
```

- Trim retransmit buffer during live sessions when app-level ACKs arrive:

```go
seq.Ack(ackedSeq)
```

### Safety

- Keep token signing key in a secret manager or environment-injected secret.
- Enforce payload size limits at adapter or app boundary.
- Ensure session expiry policy (`Ephemeral/Interactive/Durable`) matches risk profile.

## 3. Store and Durability Choices

- `store/memory`: fast, in-process, state lost on restart.
- `store/file`: restart persistence for single-node usage.
- For multi-node production, implement a shared external store.

## 4. Concurrency Expectations

`session.Sequencer` is safe for concurrent access, but your surrounding
application logic should still define ownership for session lifecycle transitions
and adapter close/disconnect handling.

## 5. Security Practices

- Rotate token secrets with operational procedures.
- Log rejection reasons but avoid logging full resume tokens.
- Add request-level rate limiting at your network edge.

## 6. Release Readiness Checklist

- [ ] Functional tests and race tests pass in CI
- [ ] Observability dashboards and alerts are wired
- [ ] Staleness window configured for your latency/reconnect profile
- [ ] Ack trimming integrated into your app protocol
- [ ] Secret handling and rotation defined
- [ ] Load/reconnect storm testing completed

## 7. Companion Operational Docs

- `docs/runbook.md` — incident response and operations template.
- `docs/alerts.md` — starter metric names and Prometheus-style alert rules.

