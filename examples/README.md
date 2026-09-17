# Examples

Worked examples for cluster-api-provider-verda. All of them assume a
management cluster with Cluster API, the kubeadm providers and this provider
installed (see the main README), and these variables exported:

```sh
export VERDA_CLIENT_ID=...          # for the cloud controller manager in the workload cluster
export VERDA_CLIENT_SECRET=...
export VERDA_LOCATION=FIN-03
export VERDA_SSH_KEY_ID=<verda ssh key id>
# Optional: the templates default to the current images built by packer/
# (VERDA_OS_VOLUME_ID=k8s-node-v1.35.8-ubuntu-24.04-20260917-5a5059e,
#  VERDA_GPU_OS_VOLUME_ID=k8s-gpu-node-v1.35.8-cuda12-8-20260917-5a5059e); override with an ID or exact name.
export KUBERNETES_VERSION=v1.35.8
```

| Example | What it shows |
|---|---|
| [ha-cluster](ha-cluster/) | Three control planes behind the provider-managed load balancer, one worker pool, CNI, and how to reach the cluster. |
| [autoscaling](autoscaling/) | cluster-autoscaler in the management cluster scaling a worker MachineDeployment, including from zero. |
| [gpu-workers](gpu-workers/) | A GPU worker pool with a data volume and spot pricing, plus the NVIDIA device plugin. |

Each directory has a `README.md` with the exact commands and the manifests
they apply. Manifests use `clusterctl generate` variable syntax (`${VAR}` and
`${VAR:=default}`), so render them with `clusterctl generate cluster <name>
--from <file>` or `clusterctl generate yaml --from <file>`.
