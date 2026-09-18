# Versions come from versions.pkrvars.json (shared with build.sh / CI).

variable "kubernetes_version" {
  type = string
  validation {
    condition     = can(regex("^1\\.35\\.[0-9]+$", var.kubernetes_version))
    error_message = "This template is pinned to the 1.35 series."
  }
}

variable "kubernetes_cni_deb_version" {
  type = string
}

variable "containerd_version" {
  type = string
}

variable "runc_version" {
  type = string
}

variable "crictl_version" {
  type = string
}

variable "image_builder_ref" {
  description = "kubernetes-sigs/image-builder git ref; build.sh checks it out to image_builder_dir."
  type        = string
}

variable "ansible_core_version" {
  description = "Installed by build.sh on the host; unused by the template."
  type        = string
}

variable "image_builder_dir" {
  type    = string
  default = ".image-builder"
}

variable "location" {
  type    = string
  default = "FIN-03"
}

variable "instance_type" {
  description = "Build instance type; any in-stock type works, the volume boots on any type."
  type        = string
  default     = "CPU.4V.16G"
}

variable "source_image" {
  description = "Verda image_type to start from (stage 1), or for the GPU stage the ID of a fresh clone of the node image (build.sh build-gpu makes it)."
  type        = string
  default     = "24.04.base"
}

variable "gpu_instance_type" {
  description = "Instance type the GPU stage builds on. A GPU type, so the built kernel modules are loaded and the driver and container runtime are verified against real hardware before the image is captured."
  type        = string
  default     = "1RTXPRO6000.30V"
}

variable "gpu" {
  description = "Stage 2: layer the NVIDIA driver, CUDA and the container toolkit on a node image clone given as source_image. The volume is named k8s-gpu-node-*."
  type        = bool
  default     = false
}

variable "source_node_image" {
  description = "Stage 2: name of the node image the clone in source_image was taken from (recorded in the manifest and the volume name)."
  type        = string
  default     = ""
}

variable "ssh_private_key_file" {
  description = "Stage 2: private half of the key baked into the node image (ssh.private_key in the SOPS file); Verda cannot inject keys into an instance booted from an existing OS volume, so Packer logs in with this one."
  type        = string
  default     = ""
}

variable "nvidia_driver_branch" {
  description = "NVIDIA driver branch installed by the GPU stage (nvidia-open-<branch>; from versions.pkrvars.json)."
  type        = string
}

variable "cuda_version" {
  description = "CUDA toolkit version installed by the GPU stage (cuda-toolkit-<version>; from versions.pkrvars.json)."
  type        = string
}

variable "nvidia_container_toolkit_version" {
  description = "nvidia-container-toolkit apt package version installed by the GPU stage (from versions.pkrvars.json)."
  type        = string
}

variable "os_volume_size" {
  description = "GB. Kept at Verda's minimum (the API rejects less: 'Specified storage size is too low'; 20 failed, 50 is the CLI's default): clones take this long to make and instances booted from the image can't be smaller — the cluster's os_volume_size grows them on first boot."
  type        = number
  default     = 50
}

variable "is_spot" {
  type    = bool
  default = false
}

variable "ssh_key_ids" {
  description = "Verda SSH key IDs baked into /root/.ssh/authorized_keys; build.sh defaults to every key in the project."
  type        = list(string)
  default     = []
}

variable "image_name" {
  description = "Volume name; default k8s-node-v<kubernetes>-<source_image>-<build_id>, or k8s-gpu-node-v<kubernetes>-cuda<cuda>-<build_id> for the GPU stage."
  type        = string
  default     = ""
}

variable "build_id" {
  type    = string
  default = ""
}

variable "artifact_locations" {
  description = "Extra datacenters to clone the image into."
  type        = list(string)
  default     = []
}
