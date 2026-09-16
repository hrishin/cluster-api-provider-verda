# Autoscaling workers with cluster-autoscaler

Runs cluster-autoscaler in the management cluster with the `clusterapi`
provider, scaling the `demo` cluster's worker MachineDeployment between 0 and
5 replicas. Start from the [ha-cluster](../ha-cluster/) example.

```sh
# Mark the node group.
kubectl annotate machinedeployment demo-md-0 \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size=0 \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size=5

# Install cluster-autoscaler; it reads Cluster API objects in-cluster and the
# workload cluster through the demo-kubeconfig Secret that KubeadmControlPlane maintains.
helm repo add autoscaler https://kubernetes.github.io/autoscaler
helm upgrade --install cluster-autoscaler autoscaler/cluster-autoscaler -f values.yaml
kubectl apply -f rbac.yaml    # lets it read VerdaMachineTemplates for scale-from-zero capacity
```

Scale-from-zero works because the provider fills
`VerdaMachineTemplate.status.capacity` (cpu, memory, `nvidia.com/gpu`) from the
Verda instance type catalog:

```sh
kubectl get verdamachinetemplate demo-md-0 -o jsonpath='{.status.capacity}'
```

Try it: deploy something that does not fit and watch a worker appear.

```sh
kubectl --kubeconfig demo.kubeconfig create deployment cpu-hog --image=registry.k8s.io/pause:3.10 --replicas=6
kubectl --kubeconfig demo.kubeconfig set resources deployment cpu-hog --requests=cpu=1
watch kubectl get machinedeployment demo-md-0 machines
```

`values.yaml` shortens the scale-down timers to two minutes for
demonstration; remove `extraArgs` for production defaults.
