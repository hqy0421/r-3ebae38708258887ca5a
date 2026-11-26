# Storage Class Groups in pac-quota-controller

## Overview

Some storage classes are backed by the same physical storage — for example two
tiers or two provisioners over one storage array. Administrators usually want to
keep per-class limits (so one class cannot starve the other) while **also**
capping the combined consumption of the pair.

`spec.storageClassGroups` adds that second layer. It declares groups of storage
classes with their own combined limits, expressed as additional resource keys in
`hard`:

| Key | Meaning |
|---|---|
| `<group>.storageclassgroup.storage.k8s.io/requests.storage` | combined storage of every class in the group |
| `<group>.storageclassgroup.storage.k8s.io/persistentvolumeclaims` | combined PVC count of every class in the group |

Per-class limits keep using the classic
`<class>.storageclass.storage.k8s.io/{requests.storage,persistentvolumeclaims}`
keys, unchanged.

A PVC charged against a class in a group consumes **both** its class budget and
its group budget. If either would be exceeded, the request is denied — the class
limit protects the class, the group limit protects the shared storage.

## Example

```yaml
apiVersion: quota.powerapp.cloud/v1alpha1
kind: ClusterResourceQuota
metadata:
  name: storage-quota
spec:
  namespaceSelector:
    matchLabels:
      team: data
  storageClassGroups:
    - name: shared-ssd
      storageClasses: [fast-ssd, premium-ssd]
  hard:
    # Combined budget for the shared array (both classes together)
    shared-ssd.storageclassgroup.storage.k8s.io/requests.storage: 200Gi
    shared-ssd.storageclassgroup.storage.k8s.io/persistentvolumeclaims: "50"
    # Per-class budgets, as before
    fast-ssd.storageclass.storage.k8s.io/requests.storage: 120Gi
    fast-ssd.storageclass.storage.k8s.io/persistentvolumeclaims: "40"
    premium-ssd.storageclass.storage.k8s.io/requests.storage: 120Gi
    premium-ssd.storageclass.storage.k8s.io/persistentvolumeclaims: "40"
```

A PVC on `fast-ssd` requesting 10Gi is admitted only if
`fast-ssd` has 10Gi free **and** `shared-ssd` has 10Gi free.

Group membership is optional per class: a class absent from every group keeps
the classic per-class-only behavior, and per-class limits are optional too —
declaring a group by itself caps only the combined usage.

## Behavior

### Admission (webhooks)

- **Create** charges the full requested storage plus one PVC against the global
  (`requests.storage`, `persistentvolumeclaims`), the class, and (when the class
  is grouped) the group keys.
- **Update (resize)** charges only the actual storage difference
  (`new - old`). PVC count is unchanged by a resize, so no count check runs.
  Shrinks charge nothing.
- Every check is evaluated independently against `status.total.used`; the first
  failure denies the request with a message naming the layer that failed (`storage
  class 'x'`, `storage class group 'y'`, or the quota-wide limit).
- Missing status usage fails open (allows), as it does for every other resource.

### Reconciliation (controller)

For each selected namespace the controller buckets PVCs by storage class once,
then derives a second bucket per group from the member classes only. Group usage
is therefore the sum over **stored volumes whose class belongs to the group** —
PVCs of ungrouped classes and PVCs on other groups never contribute. Results are
published in `status.total.used` and the per-namespace status maps under the
group keys, exactly like per-class keys.

## Validation rules

Enforced at CRQ admission; group name and list shape are additionally enforced
by the CRD's OpenAPI schema, so they hold even if the webhook is unavailable.

- each group needs a name (lowercase DNS-1123 subdomain, ≤ 200 characters, so the
  derived resource key stays within Kubernetes' 253-character limit) and a
  non-empty `storageClasses` list
- group names are unique within the quota
- a storage class may appear **at most once in total** — listing it in two groups
  (or twice in one group) is rejected, so a volume never has an ambiguous group
- a group key in `hard` must name a declared group; typos are rejected rather
  than silently enforcing nothing

Group keys are classified as standard (non-pod) resources, so quotas combining
`scopes`/`scopeSelector` with group limits in `hard` are rejected just like
quotas mixing scopes with other non-pod resources (see
[docs/quota-scopes.md](quota-scopes.md)).

## Notes

- A ready-to-apply manifest is in
  [examples/storage-class-groups-example.yaml](../examples/storage-class-groups-example.yaml).
- Group keys are plain `hard` entries: they appear in `status`, metrics, and
  events like every other resource, with no special-casing.
- A storage class may belong to a group and simultaneously keep per-class limits
  in `hard`; neither is required when the other is declared.
- Group membership is a quota-level concept. The same class may be grouped
  differently by a different quota.
