# udn-density-pods-stage

Variant of `udn-density-pods` that uses kube-burner's **object grouping** feature to order object creation within a single job. All iterations of group N complete before group N+1 begins.

## How it differs from udn-density-pods

| | udn-density-pods | udn-density-pods-stage |
|---|---|---|
| Jobs | 3 jobs (create UDN+DS, remove DS, deploy pods) | 1 job with 3 object groups |
| Object ordering | Implicit via job sequence | Explicit via `group: N` on each object |
| DaemonSet lifecycle | Separate delete job | `options.gc: true` on group 2 with pause before/after |

## Object Groups

```
Group 1 ─── UDN (L2 or L3)
             └─ Waits for NetworkAllocationSucceeded

Group 2 ─── DaemonSet (image pre-pull)
             └─ options: { gc: true, pauseBeforeGC: 1m, pauseAfterGC: 1m }
                 Automatically deleted after pause, before group 3

Group 3 ─── Workload
             ├─ NetworkPolicies (deny-all, allow-from-clients) [unless --simple]
             ├─ Services (5 replicas) [unless --simple]
             ├─ Server deployments (3 x 2 pods)
             └─ Client deployments (2 x 2 pods, readiness probes)
```

## Quick Try Examples

**Smallest run (10 UDNs, L3 default):**
```bash
kube-burner-ocp udn-density-pods-stage --iterations=10
```

**Layer 2 with local metrics:**
```bash
kube-burner-ocp udn-density-pods-stage \
  --iterations=50 \
  --layer3=false \
  --local-indexing
```

**Simple mode (no NetworkPolicies or Services, just pods):**
```bash
kube-burner-ocp udn-density-pods-stage \
  --iterations=25 \
  --simple
```

**Medium run with churn:**
```bash
kube-burner-ocp udn-density-pods-stage \
  --iterations=50 \
  --churn-duration=20m \
  --churn-percent=10 \
  --churn-delay=2m
```

**With pprof and OpenSearch indexing:**
```bash
kube-burner-ocp udn-density-pods-stage \
  --iterations=100 \
  --pprof \
  --pprof-interval=5m \
  --es-server=https://opensearch.example.com \
  --es-index=udn-density-pods-stage
```

**Dry run (extract config to inspect without running):**
```bash
kube-burner-ocp udn-density-pods-stage --iterations=10 --extract
```

## CLI Flags

Same as `udn-density-pods`. Run `kube-burner-ocp udn-density-pods-stage --help` for the full list.
