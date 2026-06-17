# HyperFleet desire-based delivery — POC walkthrough

A live capture of the desire-based delivery POC running end-to-end on the
GKE dev cluster (`gke_hcm-hyperfleet_us-central1-a_hyperfleet-dev-croche`)
on 2026-06-17 14:33 UTC. Every code-block in this doc is real output —
not a mock-up — pulled from `scratch/showcase/captures/`.

If you only have five minutes, read the **What is this** section, skim the
**Apply flow** code-blocks, and read the **Delete flow with cleanup**
section in full. That last one is the bit we landed this week.

---

## What is this

We're replacing Maestro as the way HyperFleet delivers Kubernetes
resources to target clusters. Instead of

> `CloudEvent → adapter → Maestro server (Postgres) → MQTT → OCM agent → ManifestWork`

we do

> `CloudEvent → adapter → desire store (Redis) → applier on target cluster → kube-apiserver`

**The database is the transport.** Three small JSON document types live in
the store: `ApplyDesire`, `DeleteDesire`, `ReadDesire`. The adapter writes
spec; the applier writes status; the adapter alone removes desires.

Inspired by ARO-HCP's `kube-applier`. POC repos (all on
`poc/desire-based-delivery`):

- [github.com/ciaranRoche/hyperfleet-applier](https://github.com/ciaranRoche/hyperfleet-applier) — new reference agent
- [github.com/ciaranRoche/hyperfleet-adapter](https://github.com/ciaranRoche/hyperfleet-adapter) — `desireclient.Client` transport
- [github.com/ciaranRoche/hyperfleet-infra](https://github.com/ciaranRoche/hyperfleet-infra) — adapter configs + Redis chart

Diagrams in the team Obsidian vault: `Work/Projects/HyperFleet/Planning/Maestro Replacement/POC Diagrams/POC - 01..09`.

---

## The stack at rest

Everything runs in the `hyperfleet` namespace on the GKE dev cluster.
Three adapters write desires for different resource shapes; one applier
consumes them.

```
NAME                                             IMAGE                                                STATUS
adapter1-hyperfleet-adapter-…                    quay.io/croche/hyperfleet-adapter:desire-cleanup     Running
adapter2-hyperfleet-adapter-…                    quay.io/croche/hyperfleet-adapter:desire-cleanup     Running
adapter3-hyperfleet-adapter-…                    quay.io/croche/hyperfleet-adapter:desire-cleanup     Running
hyperfleet-applier-…                             quay.io/croche/hyperfleet-applier:desire-cleanup     Running
hyperfleet-api-…                                 quay.io/.../hyperfleet-api:dev                       Running
clusters-hyperfleet-sentinel-…                   quay.io/.../hyperfleet-sentinel:dev                  Running
nodepools-hyperfleet-sentinel-…                  quay.io/.../hyperfleet-sentinel:dev                  Running
hyperfleet-api-postgresql-…                      docker.io/library/postgres:14.2                      Running
redis-…                                          redis:7-alpine                                       Running
```

Redis is empty before we start:

```bash
$ kubectl exec -n hyperfleet redis-… -- redis-cli KEYS '*'
(empty)
```

---

## Apply flow — `POST /clusters`

```bash
$ ./01-create-cluster.sh
HTTP 201 — created
Cluster ID:  019ed5c9-75f7-7a2e-89e1-114bfa2631d2
Generation:  1
```

### What the adapter does

The sentinel picks up the new cluster, publishes a CloudEvent, and
**adapter2** consumes it. Instead of building a ManifestWork, it renders
plain K8s manifests (a `Namespace` and a `ConfigMap`) and writes them as
`ApplyDesires` to Redis. Each `ApplyDesire` auto-creates a paired
`ReadDesire` so status can flow back.

```
2026/06/17 13:33:32 INFO applying desire transport=desire partition=cluster1 
  desire=namespaces.…-adapter2-namespace 
  gvk="/v1, Kind=Namespace" 
  resource=…-adapter2-namespace

2026/06/17 13:33:37 INFO applying desire transport=desire partition=cluster1 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap 
  gvk="/v1, Kind=ConfigMap" 
  resource=…-adapter2-configmap
```

Adapter2 isn't alone — **adapter1** also fires (it owns a control-plane
ConfigMap in the `hyperfleet` namespace itself). Result: **3 ApplyDesires
+ 3 ReadDesires** in Redis, all in partition `cluster1`.

```
$ kubectl exec -n hyperfleet redis-… -- redis-cli KEYS 'cluster1:*'
cluster1:apply:configmaps.hyperfleet.…-adapter1-configmap
cluster1:apply:namespaces.…-adapter2-namespace
cluster1:apply:configmaps.…-adapter2-namespace.…-adapter2-configmap
cluster1:read:configmaps.hyperfleet.…-adapter1-configmap
cluster1:read:namespaces.…-adapter2-namespace
cluster1:read:configmaps.…-adapter2-namespace.…-adapter2-configmap
```

### What an ApplyDesire actually looks like

One key per desire; status is co-located with spec; CAS-versioned. Picked
one to show in full (adapter1's ConfigMap):

```json
{
  "id": {
    "partition": "cluster1",
    "name": "configmaps.hyperfleet.019ed5c9-…-adapter1-configmap"
  },
  "spec": {
    "kubeContent": "<base64 of the rendered plain ConfigMap JSON>",
    "targetItem": {
      "group": "",
      "version": "v1",
      "resource": "configmaps",
      "namespace": "hyperfleet",
      "name": "019ed5c9-…-adapter1-configmap"
    }
  },
  "status": {
    "conditions": [
      {
        "type": "Successful",
        "status": "True",
        "lastTransitionTime": "2026-06-17T13:35:25Z",
        "reason": "Applied",
        "message": "resource applied via SSA"
      }
    ]
  },
  "version": 26
}
```

> **Contract reminder.** The adapter wrote `spec.kubeContent` and
> `spec.targetItem`. The applier wrote `status.conditions`. They meet at
> the desire ID. Neither side reaches into the other's part of the
> document.

### What the applier does

The applier polls Redis every 5 seconds. On each tick it lists desires
for partition `cluster1` and reconciles them.

```
2026-06-17T13:42:40 INFO applying resource 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap 
  type=apply gvr="/v1, Resource=configmaps"

2026-06-17T13:42:40 INFO resource applied successfully 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap type=apply

2026-06-17T13:42:45 INFO applying resource 
  desire=namespaces.…-adapter2-namespace type=apply 
  gvr="/v1, Resource=namespaces"

2026-06-17T13:42:45 INFO resource applied successfully 
  desire=namespaces.…-adapter2-namespace type=apply
```

The SSA call uses `Force=true`, so on every tick the resource is rewritten
to match the desire. That's idempotent and self-healing — if someone
mutates the resource directly on the target cluster, the next tick
restores it.

### What ReadDesire status looks like

The applier also processes `ReadDesires` on each tick. For each, it `Gets`
the live K8s object and writes it into `status.kubeContent`. That's how
the adapter sees the live state of resources on a different cluster
without ever talking to that cluster's kube-apiserver.

Decoded `kubeContent` from one of the ReadDesires:

```json
{
  "kind": "ConfigMap",
  "metadata": {
    "name": "019ed5c9-…-adapter1-configmap",
    "namespace": "hyperfleet",
    "labels": {
      "hyperfleet.io/adapter": "adapter1",
      "hyperfleet.io/cluster-id": "019ed5c9-…",
      "hyperfleet.io/cluster-name": "poc-desire-test"
    }
  },
  "data": {
    "cluster_id": "019ed5c9-…",
    "cluster_name": "poc-desire-test"
  }
}
```

### What the API sees

After the round-trip closes, adapter2 PUTs status back. From the API's
point of view:

```json
{
  "adapter": "adapter2",
  "observed_generation": 1,
  "conditions": [
    { "type": "Applied",   "status": "True",  "reason": "Applied",            "message": "All resources applied successfully" },
    { "type": "Available", "status": "True",  "reason": "AllResourcesAvailable", "message": "All resources available on target cluster" },
    { "type": "Health",    "status": "True",  "reason": "Healthy",            "message": "Adapter execution completed successfully" },
    { "type": "Finalized", "status": "False", "reason": "ResourceActive",     "message": "Resource is active, not scheduled for deletion" }
  ],
  "data": {
    "configmap": { "clusterId": "019ed5c9-…", "name": "…-adapter2-configmap" },
    "namespace": { "name": "…-adapter2-namespace", "phase": "Active" }
  }
}
```

`Applied=True, Available=True, Health=True`. The cluster is live. The
round-trip is closed: API → sentinel → adapter → desire → applier → kube
→ desire (status) → adapter → API.

---

## Delete flow — `DELETE /clusters/{id}` (the marquee)

This is the part we fixed this week. The key insight is that one
delete event is not enough — the adapter needs *two* event-driven passes:
one to post the `DeleteDesire`, and another (after the applier has run)
to clean up the keys and report `Finalized=True`.

```bash
$ ./10-delete-cluster.sh
HTTP 202 — accepted
{
  "id": "019ed5c9-…",
  "deleted_time": "2026-06-17T13:47:48.392995Z",
  "generation": 2
}
```

### Event 1 — adapter posts a DeleteDesire and waits

```
2026/06/17 13:48:02 INFO deleting via desire transport=desire partition=cluster1 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap 
  resource=…-adapter2-configmap

13:48:02 DEBUG Resource[clusterConfigMap] waiting for applier to confirm deletion
```

The adapter's `desireclient.DeleteResource` does three things in quick
succession on Redis: post a `DeleteDesire`, remove the `ApplyDesire`, and
**keep the `ReadDesire`** so the applier can mirror the resource's
disappearance. Then it probes the `DeleteDesire` it just posted — no
`Successful` condition yet because the applier hasn't polled — and
reports `Finalized=False` to the API.

### Between events — applier confirms deletion

Three seconds later, the applier's next poll cycle picks up the
`DeleteDesires`:

```
2026-06-17T13:48:05 INFO deleting resource 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap type=delete

2026-06-17T13:48:05 INFO resource confirmed deleted after delete call 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap type=delete

2026-06-17T13:48:10 INFO resource confirmed deleted 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap type=delete
```

It deletes the K8s resource, re-`Gets` to confirm it's gone past
finalizers, and writes `Successful=True, Reason=Deleted` back on the
`DeleteDesire`. The applier does **not** remove the desire — that's the
adapter's job.

(The namespace takes longer because it cascades child cleanup):

```
2026-06-17T13:48:15 INFO deleting resource desire=namespaces.…-adapter2-namespace
2026-06-17T13:48:30 INFO resource confirmed deleted desire=namespaces.…-adapter2-namespace
```

### Event 2 — adapter cleans up

The sentinel re-fires because the cluster is still not `Finalized` in
the API. Adapter2's next event sees the applier's confirmation and
cleans up:

```
2026/06/17 13:48:12 INFO cleaning up delete desire after confirmed deletion 
  transport=desire partition=cluster1 
  desire=configmaps.…-adapter2-namespace.…-adapter2-configmap

2026/06/17 13:48:32 INFO cleaning up delete desire after confirmed deletion 
  transport=desire partition=cluster1 
  desire=namespaces.…-adapter2-namespace
```

There are actually **two cleanup entry points** in the executor and you
can see them both in the log lines above:

| Path | Trigger | Where in code |
|---|---|---|
| `confirmDesireDelete` | The SAME event that posted the DeleteDesire sees `Successful=True` because the applier was fast | `internal/executor/resource_executor.go` |
| `cleanupOrphanDesires` | A LATER event sees the resource gone via the `ReadDesire` mirror — takes the early-return branch | `internal/executor/resource_executor.go` |

Both call the same cleanup methods on the desire client; just different
event-arrival timings. The symmetry is what makes the design robust to
broker latency.

### Final state — Redis empty, API hard-deleted, K8s clean

```
$ kubectl exec -n hyperfleet redis-… -- redis-cli KEYS 'cluster1:*'
(empty)

$ ./08-query-api.sh
WARN  Cluster 'poc-desire-test' not found

$ kubectl get namespaces -l hyperfleet.io/cluster-id=019ed5c9-…
No resources found

$ kubectl get configmaps -A -l hyperfleet.io/cluster-id=019ed5c9-…
No resources found
```

End-to-end clean.

---

## The two invariants

If you remember nothing else from this doc, remember these:

1. **Adapter owns desire lifecycle.** It writes spec, it removes desires.
   That's where all the policy lives — what to apply, when to delete,
   how to interpret status.

2. **Applier is read-only on spec, write-only on status. Never removes a
   desire.** That's what keeps it small enough to swap out. A partner
   team could implement their own applier against the same `SpecStore` /
   `StatusStore` interfaces and pass a conformance suite without touching
   adapter or store code.

Everything else in the design — the three desire types, the partition
key, the pluggable store backends — follows from those two invariants.

---

## Open

- ADR in the `architecture/` repo capturing Option C + the contract
- Conformance suite for partner agents
- Production store backend choice (Redis is fine for POC; Postgres or
  Cosmos with the same store interfaces for production)
- Perf + scale testing (POC runs single partition / single applier)

---

## Where the captures live

Every code-block above pulls from real files in
`scratch/showcase/captures/`:

```
captures/
├── 00-stack.txt
├── 00-redis-empty.txt
├── apply/
│   ├── 01-create-output.txt
│   ├── 02-redis-keys.txt
│   ├── 02-apply-desire-configmap.json   ← the full ApplyDesire object
│   ├── 03-read-desire-configmap.json    ← the ReadDesire with mirrored status
│   ├── 03-read-desire-live-object.json  ← decoded kubeContent
│   ├── 04-api-status.txt
│   ├── 05-adapter-logs.txt
│   ├── 06-applier-logs.txt
│   └── 07-kube-resources.txt
└── delete/
    ├── 01-delete-output.txt
    ├── 02-redis-just-after-trigger.txt
    ├── 05-redis-final.txt
    ├── 06-adapter-delete-logs.txt
    ├── 07-applier-delete-logs.txt
    ├── 08-api-final.txt
    └── 09-kube-final.txt
```

If anyone asks "did this actually happen?" — yes, these are the receipts.
