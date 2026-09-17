# cluster-api-provider-verda

Installs the [Cluster API infrastructure provider for Verda Cloud](https://github.com/hrishin/cluster-api-provider-verda):
the controller manager, its RBAC, the admission webhooks (with a cert-manager
certificate) and, by default, the CRDs.

## Prerequisites

- Cluster API core and the kubeadm providers (`clusterctl init --core cluster-api --bootstrap kubeadm --control-plane kubeadm`)
- cert-manager (installed by `clusterctl init`) unless `webhooks.enabled=false`

## Install

```sh
helm install capv oci://registry-1.docker.io/hriships/cluster-api-provider-verda \
  --version <version> --namespace capv-system --create-namespace \
  --set credentials.create=true \
  --set credentials.clientId=$VERDA_CLIENT_ID \
  --set credentials.clientSecret=$VERDA_CLIENT_SECRET
```

Or reference a Secret you manage (keys `client-id`, `client-secret`, optional `base-url`):

```sh
helm install capv oci://registry-1.docker.io/hriships/cluster-api-provider-verda \
  --version <version> -n capv-system --create-namespace \
  --set credentials.existingSecret=verda-credentials
```

When the CRDs are managed by the `cluster-api-provider-verda-crds` chart (or
by clusterctl), add `--set crds.install=false`.

## Values

| Value | Default | Description |
|---|---|---|
| `crds.install` | `true` | Install the CRDs from this chart. |
| `crds.keep` | `true` | Keep the CRDs (and all custom resources) on `helm uninstall`. |
| `image.repository` | `docker.io/hriships/cluster-api-provider-verda` | Manager image. |
| `image.tag` | chart `appVersion` | Manager image tag. |
| `credentials.create` | `false` | Create the credentials Secret from `credentials.clientId` / `clientSecret` / `baseUrl`. |
| `credentials.existingSecret` | `""` | Name of an existing credentials Secret. Without any credentials every `VerdaCluster` needs `spec.identityRef`. |
| `manager.watchFilter` | `""` | Only reconcile objects labelled `cluster.x-k8s.io/watch-filter=<value>`. |
| `manager.namespace` | `""` | Only reconcile one namespace. |
| `manager.concurrency` | `10` | Concurrent reconciles per controller. |
| `manager.leaderElection` | `true` | Enable leader election. |
| `manager.resources` | see values | Manager resources. |
| `webhooks.enabled` | `true` | Serve validating/defaulting webhooks. |
| `webhooks.certManager.enabled` | `true` | Issue the serving certificate with cert-manager (self-signed Issuer). |
| `webhooks.certManager.issuerRef` | `{}` | Use an existing Issuer/ClusterIssuer instead. |
| `webhooks.certManager.existingSecret` | `""` | TLS Secret to use when cert-manager is disabled. |
| `metrics.enabled` | `false` | Expose the authenticated metrics endpoint and Service. |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` (needs `metrics.enabled` and the monitoring CRDs). |
| `metrics.serviceMonitor.labels` | `{}` | Labels for the ServiceMonitor, e.g. `release: kube-prometheus-stack`. |
| `metrics.serviceMonitor.prometheusServiceAccount.name` | `""` | Bind the `metrics-reader` ClusterRole to this Prometheus ServiceAccount. |
| `serviceAccount.create` / `rbac.create` | `true` | Create the ServiceAccount / RBAC. |

The CRD templates and the manager ClusterRole are generated from `config/`
by `make sync-charts`.
