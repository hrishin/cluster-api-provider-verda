# Kubernetes 1.35 node image for Verda, built in two stages and captured as
# detached OS volumes (Verda's custom images):
#   1. node:     a stock Ubuntu instance provisioned with ansible/verda-node.yml
#   2. gpu-node: a clone of the node image provisioned on a GPU instance with
#                ansible/verda-gpu-node.yml, which also verifies the driver and
#                the container runtime on the hardware (-var gpu=true;
#                build.sh build-gpu makes the clone and cleans it up)
# Run via build.sh or the node-image workflow.

packer {
  required_version = ">= 1.11.0"
  required_plugins {
    verda = {
      version = "~> 0.1.2"
      source  = "github.com/thevilledev/verda"
    }
    ansible = {
      version = "~> 1.1"
      source  = "github.com/hashicorp/ansible"
    }
  }
}

locals {
  build_id          = var.build_id != "" ? var.build_id : formatdate("YYYYMMDD-hhmm", timestamp())
  flavor            = var.gpu ? "gpu-node" : "node"
  default_name      = var.gpu ? "k8s-gpu-node-v${var.kubernetes_version}-cuda${var.cuda_version}-${local.build_id}" : "k8s-node-v${var.kubernetes_version}-${var.source_image}-${local.build_id}"
  image_name        = var.image_name != "" ? var.image_name : local.default_name
  kubernetes_series = "v${join(".", slice(split(".", var.kubernetes_version), 0, 2))}"

  # image-builder's role variables (images/capi/packer/config/*.json), pinned to 1.35.
  ansible_vars = {
    build_target        = "raw"
    python_path         = ""
    custom_role_names   = ""
    node_ansible_tmpdir = ""

    kubernetes_source_type                      = "pkg"
    kubernetes_cni_source_type                  = "pkg"
    kubernetes_series                           = local.kubernetes_series
    kubernetes_semver                           = "v${var.kubernetes_version}"
    kubernetes_deb_version                      = "${var.kubernetes_version}-1.1"
    kubernetes_deb_repo                         = "https://pkgs.k8s.io/core:/stable:/${local.kubernetes_series}/deb/"
    kubernetes_deb_gpg_key                      = "https://pkgs.k8s.io/core:/stable:/${local.kubernetes_series}/deb/Release.key"
    kubernetes_cni_deb_version                  = var.kubernetes_cni_deb_version
    kubernetes_cni_semver                       = "v${split("-", var.kubernetes_cni_deb_version)[0]}"
    kubernetes_cni_http_source                  = "https://github.com/containernetworking/plugins/releases/download"
    kubernetes_http_source                      = "https://dl.k8s.io/release"
    kubernetes_container_registry               = "registry.k8s.io"
    kubernetes_apiserver_port                   = "6443"
    kubeadm_template                            = "etc/kubeadm.yml"
    kubernetes_load_additional_imgs             = false
    kubernetes_enable_automatic_resource_sizing = false
    crictl_version                              = var.crictl_version

    containerd_version                     = var.containerd_version
    runc_version                           = var.runc_version
    containerd_service_url                 = "https://raw.githubusercontent.com/containerd/containerd/refs/tags/v${var.containerd_version}/containerd.service"
    containerd_cri_socket                  = "/var/run/containerd/containerd.sock"
    containerd_additional_settings         = ""
    containerd_enable_limit_no_file        = false
    containerd_gvisor_runtime              = false
    containerd_gvisor_version              = "latest"
    containerd_image_pull_progress_timeout = ""
    containerd_wasm_shims_runtimes         = ""
    containerd_wasm_shims_runtime_versions = "{}"
    containerd_wasm_shims_sha256           = "{}"
    containerd_wasm_shims_url              = ""
    containerd_wasm_shims_version          = ""
    enable_containerd_audit                = false
    pause_image                            = "registry.k8s.io/pause:3.10.2"

    systemd_prefix     = "/usr/lib/systemd"
    sysusr_prefix      = "/usr"
    sysusrlocal_prefix = "/usr/local"

    ubuntu_repo                = "http://archive.ubuntu.com/ubuntu/"
    ubuntu_security_repo       = "http://security.ubuntu.com/ubuntu/"
    disable_public_repos       = false
    reenable_public_repos      = true
    remove_extra_repos         = false
    extra_repos                = ""
    extra_debs                 = ""
    extra_kernel_boot_params   = ""
    netplan_removal_excludes   = ""
    pip_conf_file              = ""
    http_proxy                 = ""
    https_proxy                = ""
    no_proxy                   = ""
    debug_tools                = false
    gpu_block_nouveau_loading  = false
    load_additional_components = false
    ecr_credential_provider    = false
    image_builder_version      = var.image_builder_ref
  }
}

source "verda-instance" "k8s_node" {
  instance_type = var.gpu ? var.gpu_instance_type : var.instance_type
  location_code = var.location
  image         = var.source_image
  hostname      = "packer-k8s-${local.flavor}-${local.build_id}"
  description   = "packer build of ${local.image_name}"
  is_spot       = var.is_spot

  # Stage 2 boots the clone itself; asking for a new OS volume as well makes
  # the API reject the request ("volume not found").
  os_volume_name = var.gpu ? "" : "packer-k8s-${local.flavor}-${local.build_id}-build"
  os_volume_size = var.gpu ? 0 : var.os_volume_size

  # Stage 1: project keys are baked in and Packer's temporary key is removed
  # before capture. Stage 2 boots a clone of the node image, into which Verda
  # injects no new keys (build.sh passes the keys the image already has, which
  # the API accepts), so Packer logs in with the baked key's private half.
  ssh_username              = "root"
  ssh_key_ids               = var.ssh_key_ids
  ssh_clear_authorized_keys = !var.gpu
  temporary_ssh_key_name    = var.gpu ? "" : "packer-k8s-${local.flavor}-${local.build_id}"
  skip_temporary_ssh_key    = var.gpu
  ssh_private_key_file      = var.gpu ? var.ssh_private_key_file : ""

  artifact_type                  = "os_volume"
  artifact_volume_name           = local.image_name
  artifact_volume_location_codes = var.artifact_locations
  delete_permanently             = true

  instance_timeout = "30m"
  poll_interval    = "15s"
}

build {
  name    = "k8s-node"
  sources = ["source.verda-instance.k8s_node"]

  provisioner "shell" {
    inline = [
      "cloud-init status --wait >/dev/null || true",
      "systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service 2>/dev/null || true",
      "while fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock >/dev/null 2>&1; do sleep 5; done",
    ]
  }

  provisioner "ansible" {
    playbook_file = var.gpu ? "${path.root}/ansible/verda-gpu-node.yml" : "${path.root}/ansible/verda-node.yml"
    user          = "root"
    use_proxy     = false
    ansible_env_vars = [
      "ANSIBLE_ROLES_PATH=${var.image_builder_dir}/images/capi/ansible/roles",
      "ANSIBLE_HOST_KEY_CHECKING=False",
      "ANSIBLE_NOCOLOR=True",
      # Keepalives: the driver and CUDA installs run for minutes without output,
      # and GitHub-hosted runners sit behind a NAT that drops idle connections
      # after about four minutes ("Shared connection closed").
      "ANSIBLE_SSH_ARGS=-C -o ControlMaster=auto -o ControlPersist=60s -o ServerAliveInterval=30 -o ServerAliveCountMax=10",
    ]
    extra_arguments = [
      "--extra-vars", jsonencode(local.ansible_vars),
      "--extra-vars", jsonencode({
        nvidia_driver_branch             = var.nvidia_driver_branch
        cuda_version                     = var.cuda_version
        nvidia_container_toolkit_version = var.nvidia_container_toolkit_version
      }),
    ]
  }

  # artifact_id is the volume ID in the build location.
  post-processor "manifest" {
    output     = "${path.root}/packer-manifest.json"
    strip_path = true
    custom_data = {
      image_name         = local.image_name
      kubernetes_version = var.kubernetes_version
      source_image       = var.source_image
      source_node_image  = var.source_node_image
      flavor             = local.flavor
      instance_type      = var.gpu ? var.gpu_instance_type : var.instance_type
      location           = var.location
    }
  }
}
