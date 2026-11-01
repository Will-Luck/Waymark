---
name: harbour project
description: Harbour, the team's container registry (garbage collection, quotas, per-namespace tokens). **v3.1.0 RELEASED 2026-09-16 (layer dedupe: the registry stores each layer once and counts its references; garbage collection now walks manifests by digest and deletes unreferenced blobs after a grace period). PROD UPGRADED TO 3.1.0 ON 2026-09-17 (rollback container harbour-prev on 3.0.0, config backup kept).** Prior: **v3.0.0 RELEASED 2026-07-28 with namespaces and tokens.**
modified: 2026-10-26T19:16:45.933Z
---

## 2026-10-26 -- arm64 build MERGED on main, mirrored to GitHub; the v3.2.0 release waits for Mark.

The arm64 image builds in CI next to amd64. GitHub #7 asked for it; reply there once released.

## 2026-09-17 -- v3.1.0 in production.
