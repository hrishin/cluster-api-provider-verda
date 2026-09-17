# GPU worker pool

Adds a second worker pool of `1RTXPRO6000.30V` spot instances with a 500 GB
NVMe data volume to the `demo` cluster from the [ha-cluster](../ha-cluster/)
example, then installs the NVIDIA device plugin so pods can request
`nvidia.com/gpu`.

The pool boots from the **GPU node image** (`k8s-gpu-node-*`, stage 2 of
`packer/`), which carries the NVIDIA driver, CUDA and the container toolkit
with `nvidia` as containerd's default runtime; set `VERDA_GPU_OS_VOLUME_ID`
to it.

```sh
export CLUSTER_NAME=demo
envsubst < gpu-pool.yaml | kubectl apply -f -
watch kubectl get machinedeployment,machines

kubectl --kubeconfig demo.kubeconfig apply -f https://raw.githubusercontent.com/NVIDIA/k8s-device-plugin/v0.17.0/deployments/static/nvidia-device-plugin.yml
kubectl --kubeconfig demo.kubeconfig get nodes -o custom-columns='NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu,TYPE:.metadata.labels.node\.kubernetes\.io/instance-type'
```

The data volume appears as an unformatted block device; `gpu-pool.yaml`
formats and mounts it at `/data` through `preKubeadmCommands`. A spot
instance that Verda discontinues is reported as `InstanceTerminated` on
its VerdaMachine, the cloud controller manager removes its Node and the
MachineHealthCheck replaces the Machine.
