#!/usr/bin/env python3
"""Regenerates the parts of the Helm charts that derive from config/:

- CRDs from config/crd/bases into both charts (templates/crds/*.yaml), with
  the Cluster API contract label and an optional keep annotation
- the manager ClusterRole rules from config/rbac/role.yaml into the full chart

Run via `make sync-charts`; CI checks that the result is committed.
"""
import glob
import os
import re
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CHARTS = [
    os.path.join(ROOT, "charts", "cluster-api-provider-verda-crds"),
    os.path.join(ROOT, "charts", "cluster-api-provider-verda"),
]
HEADER = "# Generated from config/ by hack/sync-charts.py. Do not edit.\n"


def crd_template(doc, wrap_install):
    doc["metadata"].setdefault("labels", {})["cluster.x-k8s.io/v1beta2"] = "v1beta1"
    doc["metadata"]["labels"]["app.kubernetes.io/name"] = "cluster-api-provider-verda"
    # controller-gen's generated annotation is kept; the keep annotation is templated.
    text = yaml.safe_dump(doc, sort_keys=False, width=1000)
    # Field descriptions may contain cloud-init placeholders such as
    # {{ ds.meta_data.hostname }}; escape them so Helm renders them literally.
    text = re.sub(r"\{\{(.*?)\}\}", lambda m: '{{ "{{" }}' + m.group(1) + '{{ "}}" }}', text)
    # Insert the templated annotation block right after "metadata:\n".
    text = text.replace(
        "metadata:\n  annotations:\n",
        "metadata:\n  annotations:\n    {{- if .Values.crds.keep }}\n    helm.sh/resource-policy: keep\n    {{- end }}\n",
        1,
    )
    out = HEADER
    if wrap_install:
        out += "{{- if .Values.crds.install }}\n"
    out += text
    if wrap_install:
        out += "{{- end }}\n"
    return out


def sync_crds():
    for chart in CHARTS:
        out_dir = os.path.join(chart, "templates", "crds")
        os.makedirs(out_dir, exist_ok=True)
        for f in glob.glob(os.path.join(out_dir, "*.yaml")):
            os.remove(f)
        wrap = chart.endswith("cluster-api-provider-verda")
        for path in sorted(glob.glob(os.path.join(ROOT, "config", "crd", "bases", "*.yaml"))):
            doc = yaml.safe_load(open(path))
            name = os.path.basename(path).replace("infrastructure.cluster.x-k8s.io_", "")
            with open(os.path.join(out_dir, name), "w") as fh:
                fh.write(crd_template(doc, wrap))


def sync_rbac():
    role = yaml.safe_load(open(os.path.join(ROOT, "config", "rbac", "role.yaml")))
    rules = yaml.safe_dump(role["rules"], sort_keys=False)
    template = HEADER + """apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "capv.fullname" . }}-manager
  labels:
    {{- include "capv.labels" . | nindent 4 }}
rules:
""" + rules
    with open(os.path.join(CHARTS[1], "templates", "clusterrole-manager.yaml"), "w") as fh:
        fh.write(template)


if __name__ == "__main__":
    sync_crds()
    sync_rbac()
    print("charts synced", file=sys.stderr)
