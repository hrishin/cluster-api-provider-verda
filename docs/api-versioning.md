# API versioning

The provider serves one API version, `infrastructure.cluster.x-k8s.io/v1beta1`,
and implements the Cluster API **v1beta2 provider contract** (the CRDs carry
the `cluster.x-k8s.io/v1beta2: v1beta1` label that maps the two).

## Stability

`v1beta1` follows the Kubernetes deprecation policy for beta APIs: fields are
added but not removed or renamed within the version, and defaults are set by
the mutating webhook rather than changed silently. Fields that would require
replacing infrastructure are immutable through the validating webhook
(`docs` in the field comments say which).

## Adding a version

When a breaking change is needed (for example renaming `osVolumeID` to a
structured `osVolume` field):

1. Add `api/v1beta2` with the new shape and mark it `+kubebuilder:storageversion`;
   keep `v1beta1` served.
2. Implement `conversion.Convertible` on the `v1beta1` types (hub-and-spoke,
   with `v1beta2` as hub) and enable the conversion webhook in
   `config/crd/kustomization.yaml` (the `[WEBHOOK]`/`[CERTMANAGER]` blocks and
   the `cainjection` scaffold markers are in place).
3. Update the CRD contract label to `cluster.x-k8s.io/v1beta2: v1beta1_v1beta2`
   so core Cluster API picks the latest version for references.
4. Bump `metadata.yaml` with a new release series; keep the contract at
   `v1beta2` unless the Cluster API contract itself moved.
5. Round-trip fuzz tests (`sigs.k8s.io/cluster-api/util/conversion`) guard the converters.

Objects created with `v1beta1` keep working across the change; `clusterctl
upgrade` handles the storage version migration.

## Reference

The CRD field documentation is generated from the Go types in
`api/v1beta1/*_types.go`; `kubectl explain verdamachine.spec` shows it once
the CRDs are installed.
