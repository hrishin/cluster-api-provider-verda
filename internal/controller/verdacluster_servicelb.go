/*
Copyright 2026 The Verda CAPI Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// The envoy service load balancer part of the VerdaCluster reconciler.

package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
	"github.com/hrishin/verda-capi/internal/loadbalancer"
)

type WorkloadClientFunc func(kubeconfigBytes []byte) (client.Client, error)

func DefaultWorkloadClient(kubeconfigBytes []byte) (client.Client, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, err
	}
	restConfig.Timeout = lbPollInterval
	return client.New(restConfig, client.Options{})
}

func (r *clusterScope) reconcileServiceLoadBalancer(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) (requeue bool, err error) {
	log := ctrl.LoggerFrom(ctx)

	keys, err := r.ensureKeys(ctx, cluster, verdaCluster, serviceLoadBalancerSecretName(verdaCluster))
	if err != nil {
		return false, err
	}

	status := &verdaCluster.Status.ServiceLoadBalancer
	instance, err := r.findInstanceByStatusOrTag(ctx, status.InstanceID, cloud.TagServiceLoadBalancer, clusterTagValue(verdaCluster))
	if err != nil {
		return false, err
	}
	if instance == nil {
		lb := verdaCluster.Spec.ServiceLoadBalancer
		instance, err = r.cloud.CreateInstance(ctx, cloud.InstanceSpec{
			Hostname:      serviceLoadBalancerHostname(verdaCluster),
			Description:   fmt.Sprintf("Cluster API service load balancer for cluster %s/%s", cluster.Namespace, cluster.Name),
			InstanceType:  orDefault(lb.InstanceType, infrav1.DefaultLoadBalancerInstanceType),
			Image:         orDefault(lb.Image, infrav1.DefaultLoadBalancerImage),
			Location:      verdaCluster.Spec.Location,
			SSHKeyIDs:     lb.SSHKeyIDs,
			StartupScript: loadbalancer.EnvoyStartupScript(keys),
			Tags: map[string]string{
				cloud.TagManagedBy:           cloud.ManagedByValue,
				cloud.TagCluster:             cluster.Namespace + "/" + cluster.Name,
				cloud.TagServiceLoadBalancer: clusterTagValue(verdaCluster),
				cloud.TagRole:                "service-load-balancer",
			},
		})
		if err != nil {
			setServiceLBNotReady(verdaCluster, "InstanceCreateFailed", err.Error())
			return false, err
		}
		log.Info("Created service load balancer instance", "instanceID", instance.ID)
	}
	status.InstanceID = instance.ID
	if instance.StartupScriptID != "" {
		status.StartupScriptID = instance.StartupScriptID
	}

	switch {
	case instance.Status == cloud.StatusRunning && instance.IP != "":
	case instanceGone(instance) || instance.Status == cloud.StatusError || instance.Status == cloud.StatusNoCapacity:
		setServiceLBNotReady(verdaCluster, "InstanceFailed", fmt.Sprintf("service load balancer instance %s is in state %q", instance.ID, instance.Status))
		return false, nil
	default:
		setServiceLBNotReady(verdaCluster, "InstanceProvisioning", fmt.Sprintf("service load balancer instance %s is in state %q", instance.ID, instance.Status))
		return true, nil
	}
	status.Address = instance.IP

	if err := r.publishServiceLoadBalancerSecret(ctx, cluster, instance.IP, keys); err != nil {
		setServiceLBNotReady(verdaCluster, "WaitingForWorkloadCluster", err.Error())
		log.V(4).Info("Could not publish service load balancer secret to the workload cluster yet", "err", err.Error())
		return true, nil
	}
	status.SecretPublished = ptr.To(true)
	conditions.Set(verdaCluster, metav1.Condition{Type: infrav1.ServiceLoadBalancerReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason})
	return false, nil
}

func (r *clusterScope) publishServiceLoadBalancerSecret(ctx context.Context, cluster *clusterv1.Cluster, address string, keys *loadbalancer.Keys) error {
	kubeconfigBytes, err := kubeconfig.FromSecret(ctx, r.Client, client.ObjectKeyFromObject(cluster))
	if err != nil {
		return fmt.Errorf("workload cluster kubeconfig not available: %w", err)
	}
	workloadClient, err := r.workloadClient(kubeconfigBytes)
	if err != nil {
		return fmt.Errorf("building workload cluster client: %w", err)
	}
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: infrav1.ServiceLoadBalancerSecretName, Namespace: metav1.NamespaceSystem},
		Data: map[string][]byte{
			"address":          []byte(address),
			"ssh-privatekey":   keys.ClientPrivateKey,
			"host-publickey":   keys.HostPublicKey,
			"instance-cluster": []byte(cluster.Namespace + "/" + cluster.Name),
		},
	}
	existing := &corev1.Secret{}
	err = workloadClient.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		return workloadClient.Create(ctx, desired)
	case err != nil:
		return fmt.Errorf("reading %s in the workload cluster: %w", client.ObjectKeyFromObject(desired), err)
	}
	if string(existing.Data["address"]) == address && string(existing.Data["ssh-privatekey"]) == string(keys.ClientPrivateKey) {
		return nil
	}
	existing.Data = desired.Data
	return workloadClient.Update(ctx, existing)
}

func (r *clusterScope) removeServiceLoadBalancerSecret(ctx context.Context, cluster *clusterv1.Cluster) {
	kubeconfigBytes, err := kubeconfig.FromSecret(ctx, r.Client, client.ObjectKeyFromObject(cluster))
	if err != nil {
		return
	}
	workloadClient, err := r.workloadClient(kubeconfigBytes)
	if err != nil {
		return
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: infrav1.ServiceLoadBalancerSecretName, Namespace: metav1.NamespaceSystem}}
	if err := workloadClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		ctrl.LoggerFrom(ctx).V(4).Info("Could not remove the service load balancer secret from the workload cluster", "err", err.Error())
	}
}

func (r *clusterScope) workloadClient(kubeconfigBytes []byte) (client.Client, error) {
	if r.WorkloadClient != nil {
		return r.WorkloadClient(kubeconfigBytes)
	}
	return DefaultWorkloadClient(kubeconfigBytes)
}

func (r *clusterScope) deleteServiceLoadBalancer(ctx context.Context, verdaCluster *infrav1.VerdaCluster) (requeue bool, err error) {
	status := &verdaCluster.Status.ServiceLoadBalancer
	instance, err := r.findInstanceByStatusOrTag(ctx, status.InstanceID, cloud.TagServiceLoadBalancer, clusterTagValue(verdaCluster))
	if err != nil {
		return false, err
	}
	if instance != nil && !instanceGone(instance) {
		if instance.Status != cloud.StatusDeleting && deleteRequestDue(verdaCluster, "servicelb", instance.Status) {
			ctrl.LoggerFrom(ctx).Info("Deleting service load balancer instance", "instanceID", instance.ID)
			if err := r.cloud.DeleteInstance(ctx, instance.ID); err != nil {
				return false, err
			}
			setServiceLBNotReady(verdaCluster, InstanceDeleteRequestedReason, fmt.Sprintf("Delete requested for service load balancer instance %s", instance.ID))
		}
		return true, nil
	}
	if instance != nil && instance.Status != cloud.StatusNotFound {
		pending, err := r.cloud.PurgeInstanceVolumes(ctx, instance.ID)
		if err != nil {
			return false, err
		}
		if pending > 0 {
			return true, nil
		}
	}
	if status.StartupScriptID != "" {
		if err := r.cloud.DeleteStartupScript(ctx, status.StartupScriptID); err != nil {
			return false, err
		}
		status.StartupScriptID = ""
	} else if err := r.cloud.DeleteStartupScriptByName(ctx, serviceLoadBalancerHostname(verdaCluster)); err != nil {
		return false, err
	}
	return false, nil
}

func (r *clusterScope) findInstanceByStatusOrTag(ctx context.Context, id, tagKey, tagValue string) (*cloud.Instance, error) {
	if id != "" {
		instance, err := r.cloud.GetInstance(ctx, id)
		if err == nil {
			return instance, nil
		}
		if !errors.Is(err, cloud.ErrNotFound) {
			return nil, err
		}
		return &cloud.Instance{ID: id, Status: cloud.StatusNotFound}, nil
	}
	instance, err := r.cloud.FindInstanceByTag(ctx, tagKey, tagValue)
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if instanceGone(instance) {
		return nil, nil
	}
	return instance, nil
}

func setServiceLBNotReady(verdaCluster *infrav1.VerdaCluster, reason, message string) {
	conditions.Set(verdaCluster, metav1.Condition{Type: infrav1.ServiceLoadBalancerReadyCondition, Status: metav1.ConditionFalse, Reason: reason, Message: message})
}

func serviceLoadBalancerHostname(verdaCluster *infrav1.VerdaCluster) string {
	return verdaCluster.Name + "-service-lb"
}

func serviceLoadBalancerSecretName(verdaCluster *infrav1.VerdaCluster) string {
	return verdaCluster.Name + "-service-lb-ssh"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
