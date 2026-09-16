# HA cluster behind the managed load balancer

Creates `demo` with three control plane nodes and one worker. The provider
creates a `CPU.4V.16G` instance running haproxy, publishes its IP as the
control plane endpoint and keeps the control plane nodes registered as
backends. No DNS is required.

```sh
export CLUSTER_NAME=demo CONTROL_PLANE_MACHINE_COUNT=3 WORKER_MACHINE_COUNT=1
clusterctl generate cluster $CLUSTER_NAME --from ../../templates/cluster-template.yaml > demo.yaml
kubectl apply -f demo.yaml

# Watch it come up (about 6-8 minutes for the control plane).
watch kubectl get cluster,verdacluster,kubeadmcontrolplane,machines,verdamachines

# The load balancer address and backends:
kubectl get verdacluster $CLUSTER_NAME -o wide
```

Once `kubeadmcontrolplane` reports `Initialized`, get the kubeconfig and
install a CNI so nodes become `Ready`:

```sh
clusterctl get kubeconfig $CLUSTER_NAME > $CLUSTER_NAME.kubeconfig
kubectl --kubeconfig $CLUSTER_NAME.kubeconfig apply -f calico.yaml      # or: apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.30.0/manifests/calico.yaml
kubectl --kubeconfig $CLUSTER_NAME.kubeconfig get nodes -o wide
```

The cloud controller manager is installed automatically (ClusterResourceSet)
and labels nodes with their zone and instance type.

Scale the control plane or the workers like any Cluster API cluster:

```sh
kubectl scale kubeadmcontrolplane $CLUSTER_NAME-control-plane --replicas=5
kubectl scale machinedeployment $CLUSTER_NAME-md-0 --replicas=3
```

Delete with `kubectl delete cluster demo`; the provider removes the
instances, their OS volume clones, startup scripts and the load balancer.
