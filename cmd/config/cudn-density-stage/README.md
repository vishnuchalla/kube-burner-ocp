# cudn-density-stage

Variant of `cudn-density` that collapses the multi-job pipeline into a **single job** using kube-burner's **object grouping** feature. All iterations of group N complete before group N+1 begins.

## How it differs from cudn-density

| | cudn-density | cudn-density-stage |
|---|---|---|
| Jobs | 5 separate jobs (create ns, create CUDNs, deploy workload, cleanup ns, cleanup CUDNs) | 1 job with 3 object groups |
| Object ordering | Implicit via job sequence | Explicit via `group: N` on each object |
| CUDN settling | `jobPause` between job 2 and 3 | `options.pauseBeforeGC` on group 1 CUDN objects |

## Object Groups

```
Group 1 ─── CUDNs (L2 or L3)
             └─ options.pauseBeforeGC: settles OVN-K before group 2

Group 2 ─── Infrastructure (all created in parallel across iterations)
             ├─ Services (cudn-svc, headless, per-server)
             ├─ NetworkPolicies (deny-all, allow-cudn-ingress/egress, allow-app-ingress/egress)
             ├─ EgressFirewall, ResourceQuota, LimitRange
             └─ Server deployment (nginx, 2 pods)

Group 3 ─── Workload
             ├─ App deployment (sampleapp, 1 pod)
             └─ Client deployment (curl, 1 pod, cross-namespace readiness probes)
```

## Quick Try Examples

**Smallest run (2 CUDNs, 10 namespaces, 40 pods):**
```bash
kube-burner-ocp cudn-density-stage --iterations=10 --namespaces-per-cudn=5
```

**Medium run with local metrics collection:**
```bash
kube-burner-ocp cudn-density-stage \
  --iterations=50 \
  --namespaces-per-cudn=5 \
  --local-indexing
```

**Layer 3 topology, larger CUDN groups:**
```bash
kube-burner-ocp cudn-density-stage \
  --iterations=100 \
  --layer3 \
  --namespaces-per-cudn=10
```

**With churn (objects mode only, namespace churn not supported):**
```bash
kube-burner-ocp cudn-density-stage \
  --iterations=50 \
  --churn-duration=30m \
  --churn-percent=20 \
  --churn-delay=1m
```

**With pprof and OpenSearch indexing:**
```bash
kube-burner-ocp cudn-density-stage \
  --iterations=100 \
  --pprof \
  --pprof-interval=5m \
  --es-server=https://opensearch.example.com \
  --es-index=cudn-density-stage
```

**Dry run (extract config to inspect without running):**
```bash
kube-burner-ocp cudn-density-stage --iterations=10 --extract
```

## CLI Flags

Same as `cudn-density`. Run `kube-burner-ocp cudn-density-stage --help` for the full list.
