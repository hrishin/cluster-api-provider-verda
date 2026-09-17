# cluster-api-provider-verda-crds

Only the CustomResourceDefinitions of
[cluster-api-provider-verda](https://github.com/hrishin/cluster-api-provider-verda)
(`VerdaCluster`, `VerdaMachine`, `VerdaMachineTemplate`, `VerdaClusterTemplate`).

Use it when CRDs are managed separately from the controller (for example by a
platform team with cluster-admin) and install the `cluster-api-provider-verda`
chart with `--set crds.install=false`.

```sh
helm install capv-crds oci://registry-1.docker.io/hriships/cluster-api-provider-verda-crds --version <version>
```

| Value | Default | Description |
|---|---|---|
| `crds.keep` | `true` | Keep the CRDs (and all custom resources) on `helm uninstall`. |

The CRDs are rendered as templates, so `helm upgrade` updates them.
