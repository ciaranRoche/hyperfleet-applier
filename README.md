# hyperfleet-applier

POC reference agent for HyperFleet's desire-based delivery model — the
replacement for Maestro-based ManifestWork delivery. Single binary, one per
target cluster, polls a partitioned desire store and reconciles against the
local kube-apiserver.

> **POC status.** Wired end-to-end on a GKE dev cluster on 2026-06-17.
> Not production-grade: simple polling (no informers), broad RBAC, no
> leader election, no metrics. The point is to validate the model.

## How it works

Three controllers run on every poll tick (default 5s):

| Controller | What it does | Status write |
|---|---|---|
| `reconcileApplyDesires`  | Parses `spec.kubeContent`, server-side applies with `Force=true` | `Successful=True, Reason=Applied` |
| `reconcileDeleteDesires` | Gets the target; if present, Deletes + re-Gets to confirm gone past finalizers | `Successful=True, Reason=Deleted` (or `WaitingForDeletion`) |
| `reconcileReadDesires`   | Gets the target; marshals the live object into `status.kubeContent` (or nil if NotFound) | `Successful=True, Reason=Synced` |

The applier never removes a desire from the store. That's the adapter's
job. Spec is read-only here, status is write-only. That contract is what
keeps the binary small enough to swap out — a partner team could implement
their own applier against the same store interface and pass a conformance
suite.

## Wire-up

Config is via environment variables:

| Var | Default | Notes |
|---|---|---|
| `REDIS_ADDRESS`        | `redis.hyperfleet.svc.cluster.local:6379` | Desire store endpoint |
| `APPLIER_PARTITION`    | `default`            | Which partition to poll — usually the target cluster name |
| `APPLIER_POLL_INTERVAL`| `5s`                 | Poll cadence |

Helm chart in `charts/hyperfleet-applier/`. Image build via the `Makefile`:

```bash
QUAY_USER=<you> make image-dev
```

## Build note

`go.mod` has a `replace` directive pointing to a sibling
`../hyperfleet-adapter` checkout — the applier imports the desire types
and Redis store from the adapter. To build this standalone you need the
adapter repo cloned next to it on the same `poc/desire-based-delivery`
branch:

```
~/wherever/
  hyperfleet-adapter/   # branch: poc/desire-based-delivery
  hyperfleet-applier/   # this repo, branch: poc/desire-based-delivery
```

Until the desire-based work lands on `main` and the types move to a
published package, this is the only way to build.

## Walkthrough

[**`docs/showcase/desire-poc-walkthrough.md`**](docs/showcase/desire-poc-walkthrough.md)
— live capture of the POC running end-to-end (apply + delete + cleanup)
on the GKE dev cluster, backed by 17 supporting files (logs, JSON
dumps, Redis snapshots). Every code-block in the walkthrough is real
output. Read this if you want to understand what was built without
running the demo yourself.

## Related

- Inspired by: [ARO-HCP kube-applier](https://github.com/Azure/ARO-HCP/tree/main/kube-applier)
