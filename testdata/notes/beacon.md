---
name: beacon
description: Beacon -- status pages and uptime alerts; checks every minute and posts incidents. Deployed at status.example.com
metadata:
  type: project
  modified: 2026-10-31T19:30:25.607Z
---

## 2026-10-31 -- v2.4.0 TAGGED + smoke PASS, NOT DEPLOYED (waits for Mark). Production still v2.3.1.

Release #24: checks by region (#12), DNS cache (#14), per-check timeouts (#11). Image
`registry.example.com/acme/beacon:v2.4.0`. No schema change: rollback is an image swap to v2.3.1
(keep the current container as `beacon-prev`). Smoke 12/12 on the staging copy.

## 2026-10-19 (~20:00 UTC) -- v2.3.1 DEPLOYED (RSS date fix #16).

Rollback container `beacon-prev` on v2.3.0.
