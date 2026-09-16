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

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/finalizers"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/predicates"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/bootstrap"
	"github.com/hrishin/verda-capi/internal/cloud"
)

const (
	// instancePollInterval is how often a provisioning instance is re-checked.
	instancePollInterval = 15 * time.Second
	// deletePollInterval is how often a deleting instance is re-checked.
	deletePollInterval = 10 * time.Second
	// instanceResyncInterval is how often a provisioned machine's instance is
	// re-checked so that an instance disappearing out of band is noticed.
	instanceResyncInterval = 5 * time.Minute
	// noCapacityRetryInterval is how long to wait before retrying a create
	// that failed for lack of capacity.
	noCapacityRetryInterval = 2 * time.Minute
)

// VerdaMachine InstanceReady condition reasons.
const (
	// InstanceProvisioningReason means the instance exists but is not running yet.
	InstanceProvisioningReason = "InstanceProvisioning"
	// InstanceTerminatedReason means the instance was discontinued or deleted
	// outside of Cluster API. The Machine needs remediation.
	InstanceTerminatedReason = "InstanceTerminated"
	// InstanceFailedReason means Verda reports the instance in an error state.
	InstanceFailedReason = "InstanceFailed"
	// NoCapacityReason means Verda had no capacity for the instance type; the
	// create is retried.
	NoCapacityReason = "NoCapacity"
)

// VerdaMachineReconciler reconciles a VerdaMachine object.
type VerdaMachineReconciler struct {
	client.Client
	Cloud cloud.Client
	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status;machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings a Verda instance in line with its VerdaMachine.
func (r *VerdaMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)

	verdaMachine := &infrav1.VerdaMachine{}
	if err := r.Get(ctx, req.NamespacedName, verdaMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if finalizerAdded, err := finalizers.EnsureFinalizer(ctx, r.Client, verdaMachine, infrav1.MachineFinalizer); err != nil || finalizerAdded {
		return ctrl.Result{}, err
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, verdaMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Waiting for Machine controller to set OwnerRef on VerdaMachine")
		return ctrl.Result{}, nil
	}
	log = log.WithValues("Machine", client.ObjectKeyFromObject(machine))
	ctx = ctrl.LoggerInto(ctx, log)

	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("VerdaMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}
	log = log.WithValues("Cluster", client.ObjectKeyFromObject(cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	if isPaused, requeue, err := paused.EnsurePausedCondition(ctx, r.Client, cluster, verdaMachine); err != nil || isPaused || requeue {
		return ctrl.Result{}, err
	}

	verdaCluster := &infrav1.VerdaCluster{}
	verdaClusterKey := client.ObjectKey{Namespace: verdaMachine.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, verdaClusterKey, verdaCluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if verdaMachine.DeletionTimestamp.IsZero() {
			log.Info("VerdaCluster is not available yet", "VerdaCluster", verdaClusterKey)
			return ctrl.Result{}, nil
		}
		verdaCluster = nil
	}

	patchHelper, err := patch.NewHelper(verdaMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchVerdaMachine(ctx, patchHelper, verdaMachine); err != nil {
			reterr = kerrorsJoin(reterr, err)
		}
	}()

	if !verdaMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, verdaMachine)
	}
	return r.reconcileNormal(ctx, cluster, verdaCluster, machine, verdaMachine)
}

func (r *VerdaMachineReconciler) reconcileNormal(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster, machine *clusterv1.Machine, verdaMachine *infrav1.VerdaMachine) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// A provisioned machine only needs its instance re-checked now and then
	// so that a spot discontinuation or an out-of-band delete is surfaced.
	if ptr.Deref(verdaMachine.Status.Initialization.Provisioned, false) {
		return r.resyncInstance(ctx, verdaMachine)
	}

	if !ptr.Deref(cluster.Status.Initialization.InfrastructureProvisioned, false) {
		log.Info("Waiting for cluster infrastructure to be provisioned")
		setInstanceReadyFalse(verdaMachine, "WaitingForClusterInfrastructure", "Cluster infrastructure is not provisioned yet")
		return ctrl.Result{}, nil
	}

	if machine.Spec.Bootstrap.DataSecretName == nil {
		log.Info("Waiting for bootstrap data to be available")
		setInstanceReadyFalse(verdaMachine, "WaitingForBootstrapData", "Machine bootstrap data secret is not available yet")
		return ctrl.Result{}, nil
	}

	instance, err := r.findInstance(ctx, verdaMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	if instance == nil {
		bootFrom := verdaMachine.Spec.Image
		if verdaMachine.Spec.OSVolumeID != "" {
			volume, err := r.ensureOSVolume(ctx, verdaCluster, verdaMachine)
			if err != nil {
				return ctrl.Result{}, err
			}
			if volume.Status != "detached" {
				log.Info("Waiting for OS volume clone", "volumeID", volume.ID, "state", volume.Status)
				setInstanceReadyFalse(verdaMachine, "WaitingForOSVolume", fmt.Sprintf("OS volume clone %s is in state %q", volume.ID, volume.Status))
				return ctrl.Result{RequeueAfter: instancePollInterval}, nil
			}
			bootFrom = volume.ID
		}

		instance, err = r.createInstance(ctx, cluster, verdaCluster, machine, verdaMachine, bootFrom)
		if err != nil {
			if errors.Is(err, cloud.ErrHostnameInUse) {
				// Not transient: another instance in the account has this
				// name. Report it and wait for the user rather than retrying.
				setInstanceReadyFalse(verdaMachine, "HostnameInUse", err.Error())
				log.Info("Hostname is in use by an unmanaged instance; not creating", "hostname", verdaMachine.Name)
				return ctrl.Result{RequeueAfter: instanceResyncInterval}, nil
			}
			setInstanceReadyFalse(verdaMachine, "InstanceCreateFailed", err.Error())
			return ctrl.Result{}, err
		}
		log.Info("Created Verda instance", "instanceID", instance.ID, "hostname", instance.Hostname)
	}

	verdaMachine.Status.InstanceID = instance.ID
	verdaMachine.Status.InstanceState = instance.Status
	r.ensureOSVolumeTag(ctx, verdaMachine)
	if instance.StartupScriptID != "" {
		verdaMachine.Status.StartupScriptID = instance.StartupScriptID
	}
	verdaMachine.Spec.ProviderID = infrav1.ProviderID(instance.Hostname)
	verdaMachine.Status.FailureDomain = instance.Location
	verdaMachine.Status.Addresses = instanceAddresses(instance)

	switch instance.Status {
	case "running":
		conditions.Set(verdaMachine, metav1.Condition{
			Type:   infrav1.InstanceReadyCondition,
			Status: metav1.ConditionTrue,
			Reason: clusterv1.ReadyReason,
		})
		verdaMachine.Status.Initialization.Provisioned = ptr.To(true)
		log.Info("Verda instance is running", "instanceID", instance.ID, "ip", instance.IP)
		return ctrl.Result{}, nil

	case "no_capacity":
		// The instance never ran. Drop it and try again later; capacity on a
		// GPU cloud comes and goes.
		log.Info("No capacity for instance type, will retry", "instanceID", instance.ID, "instanceType", verdaMachine.Spec.InstanceType)
		if err := r.Cloud.DeleteInstance(ctx, instance.ID); err != nil {
			return ctrl.Result{}, err
		}
		verdaMachine.Status.InstanceID = ""
		verdaMachine.Spec.ProviderID = ""
		verdaMachine.Status.Addresses = nil
		setInstanceReadyFalse(verdaMachine, NoCapacityReason, fmt.Sprintf("Verda has no capacity for instance type %s in %s; retrying", verdaMachine.Spec.InstanceType, verdaCluster.Spec.Location))
		return ctrl.Result{RequeueAfter: noCapacityRetryInterval}, nil

	case "error", "discontinued", "notfound":
		// Terminal from the provider's point of view: surface it and stop
		// polling. A MachineHealthCheck (or the user) remediates by deleting the Machine.
		reason := InstanceFailedReason
		if instanceGone(instance) {
			reason = InstanceTerminatedReason
		}
		setInstanceReadyFalse(verdaMachine, reason, fmt.Sprintf("Verda instance %s is in state %q", instance.ID, instance.Status))
		log.Info("Verda instance is in a terminal state", "instanceID", instance.ID, "state", instance.Status)
		return ctrl.Result{}, nil

	default:
		setInstanceReadyFalse(verdaMachine, InstanceProvisioningReason, fmt.Sprintf("Verda instance %s is in state %q", instance.ID, instance.Status))
		return ctrl.Result{RequeueAfter: instancePollInterval}, nil
	}
}

// resyncInstance re-checks a provisioned machine's instance. Initialization
// stays true (the contract forbids flipping it back); readiness is reported
// through the InstanceReady and Ready conditions.
func (r *VerdaMachineReconciler) resyncInstance(ctx context.Context, verdaMachine *infrav1.VerdaMachine) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	instance, err := r.findInstance(ctx, verdaMachine)
	if err != nil {
		return ctrl.Result{}, err
	}
	if instance == nil {
		instance = &cloud.Instance{ID: verdaMachine.Status.InstanceID, Status: "notfound"}
	}
	verdaMachine.Status.InstanceState = instance.Status
	if instance.IP != "" {
		verdaMachine.Status.Addresses = instanceAddresses(instance)
	}

	switch {
	case instance.Status == "running":
		conditions.Set(verdaMachine, metav1.Condition{Type: infrav1.InstanceReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason})
	case instanceGone(instance):
		if conditions.GetReason(verdaMachine, infrav1.InstanceReadyCondition) != InstanceTerminatedReason {
			log.Info("Verda instance is gone; the Machine needs remediation", "instanceID", instance.ID, "state", instance.Status)
		}
		setInstanceReadyFalse(verdaMachine, InstanceTerminatedReason, fmt.Sprintf("Verda instance %s is in state %q; delete the Machine to replace it", instance.ID, instance.Status))
	case instance.Status == "error":
		setInstanceReadyFalse(verdaMachine, InstanceFailedReason, fmt.Sprintf("Verda instance %s is in state %q", instance.ID, instance.Status))
	default:
		// offline, pending, provisioning after a reboot, ...: not serving right now.
		setInstanceReadyFalse(verdaMachine, "InstanceNotRunning", fmt.Sprintf("Verda instance %s is in state %q", instance.ID, instance.Status))
	}
	return ctrl.Result{RequeueAfter: instanceResyncInterval}, nil
}

// findInstance locates the instance backing verdaMachine, by recorded ID first
// and then by tag, so that a create whose result was never persisted is
// recovered rather than duplicated.
func (r *VerdaMachineReconciler) findInstance(ctx context.Context, verdaMachine *infrav1.VerdaMachine) (*cloud.Instance, error) {
	if verdaMachine.Status.InstanceID != "" {
		instance, err := r.Cloud.GetInstance(ctx, verdaMachine.Status.InstanceID)
		if err == nil {
			return instance, nil
		}
		if !errors.Is(err, cloud.ErrNotFound) {
			return nil, err
		}
		// The recorded instance is gone; report it as terminal rather than
		// silently replacing it.
		return &cloud.Instance{ID: verdaMachine.Status.InstanceID, Status: "notfound"}, nil
	}

	instance, err := r.Cloud.FindInstanceByTag(ctx, cloud.TagMachine, machineTagValue(verdaMachine))
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	// Verda keeps discontinued instances around; one we gave up on (for
	// example after no_capacity) must not be adopted again.
	if instanceGone(instance) {
		return nil, nil
	}
	return instance, nil
}

// createInstance creates the instance backing verdaMachine, booting from
// bootFrom (an image slug or a detached OS volume ID).
func (r *VerdaMachineReconciler) createInstance(ctx context.Context, cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster, machine *clusterv1.Machine, verdaMachine *infrav1.VerdaMachine, bootFrom string) (*cloud.Instance, error) {
	log := ctrl.LoggerFrom(ctx)

	bootstrapData, err := r.getBootstrapData(ctx, machine)
	if err != nil {
		return nil, err
	}
	script, err := bootstrap.ToStartupScript(bootstrapData, verdaMachine.Name)
	if err != nil {
		return nil, fmt.Errorf("converting bootstrap data to startup script: %w", err)
	}
	if len(script.Unsupported) > 0 {
		log.Info("Bootstrap data contains cloud-config sections that are not supported on Verda and were ignored", "sections", script.Unsupported)
	}

	role := "worker"
	if util.IsControlPlaneMachine(machine) {
		role = "control-plane"
	}

	spec := cloud.InstanceSpec{
		Hostname:       verdaMachine.Name,
		Description:    fmt.Sprintf("Cluster API %s node for cluster %s/%s", role, cluster.Namespace, cluster.Name),
		InstanceType:   verdaMachine.Spec.InstanceType,
		Image:          bootFrom,
		Location:       verdaCluster.Spec.Location,
		SSHKeyIDs:      verdaMachine.Spec.SSHKeyIDs,
		OSVolumeSizeGB: osVolumeSize(verdaMachine),
		Contract:       verdaMachine.Spec.Contract,
		Spot:           ptr.Deref(verdaMachine.Spec.Spot, false),
		StartupScript:  script.Script,
		Tags: map[string]string{
			cloud.TagManagedBy: cloud.ManagedByValue,
			cloud.TagCluster:   cluster.Namespace + "/" + cluster.Name,
			cloud.TagMachine:   machineTagValue(verdaMachine),
			"capi-role":        role,
		},
	}
	return r.Cloud.CreateInstance(ctx, spec)
}

// ensureOSVolume returns the per-machine clone of spec.osVolumeID, cloning it
// if needed. The clone is looked up by recorded ID first and then by name so
// a clone whose ID was never persisted is reused rather than duplicated.
func (r *VerdaMachineReconciler) ensureOSVolume(ctx context.Context, verdaCluster *infrav1.VerdaCluster, verdaMachine *infrav1.VerdaMachine) (*cloud.Volume, error) {
	log := ctrl.LoggerFrom(ctx)
	name := osVolumeName(verdaMachine)

	var volume *cloud.Volume
	var err error
	if verdaMachine.Status.OSVolumeID != "" {
		volume, err = r.Cloud.GetVolume(ctx, verdaMachine.Status.OSVolumeID)
	} else {
		volume, err = r.Cloud.FindVolumeByName(ctx, name)
	}
	if err != nil && !errors.Is(err, cloud.ErrNotFound) {
		return nil, err
	}

	if volume == nil {
		id, err := r.Cloud.CloneVolume(ctx, verdaMachine.Spec.OSVolumeID, name, verdaCluster.Spec.Location)
		if err != nil {
			conditions.Set(verdaMachine, metav1.Condition{
				Type: infrav1.OSVolumeReadyCondition, Status: metav1.ConditionFalse, Reason: "CloneFailed", Message: err.Error(),
			})
			return nil, err
		}
		log.Info("Cloned OS volume", "sourceVolumeID", verdaMachine.Spec.OSVolumeID, "volumeID", id, "name", name)
		verdaMachine.Status.OSVolumeID = id
		volume = &cloud.Volume{ID: id, Name: name, Status: "cloning"}
	}
	verdaMachine.Status.OSVolumeID = volume.ID

	if !volume.Managed {
		if err := r.Cloud.EnsureVolumeTag(ctx, volume.ID); err != nil {
			log.V(4).Info("Could not tag OS volume clone yet, will retry", "volumeID", volume.ID, "err", err.Error())
		} else {
			volume.Managed = true
		}
	}

	cond := metav1.Condition{Type: infrav1.OSVolumeReadyCondition, Status: metav1.ConditionFalse, Reason: "Cloning", Message: fmt.Sprintf("OS volume %s is in state %q", volume.ID, volume.Status)}
	if volume.Status == "detached" || volume.Status == "attached" {
		cond = metav1.Condition{Type: infrav1.OSVolumeReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason}
		if !volume.Managed {
			// Keep the condition False until the managed tag is on, so tagging is retried.
			cond = metav1.Condition{Type: infrav1.OSVolumeReadyCondition, Status: metav1.ConditionFalse, Reason: "Tagging", Message: fmt.Sprintf("OS volume %s is available but not yet tagged", volume.ID)}
		}
	}
	conditions.Set(verdaMachine, cond)
	return volume, nil
}

// ensureOSVolumeTag tags the OS volume clone as managed once it is no longer
// cloning. Tagging fails while a clone is in progress, so it is retried here on
// every reconcile until it succeeds.
func (r *VerdaMachineReconciler) ensureOSVolumeTag(ctx context.Context, verdaMachine *infrav1.VerdaMachine) {
	if verdaMachine.Status.OSVolumeID == "" || conditions.IsTrue(verdaMachine, infrav1.OSVolumeReadyCondition) {
		return
	}
	volume, err := r.Cloud.GetVolume(ctx, verdaMachine.Status.OSVolumeID)
	if err != nil {
		return
	}
	if !volume.Managed {
		if err := r.Cloud.EnsureVolumeTag(ctx, volume.ID); err != nil {
			ctrl.LoggerFrom(ctx).V(4).Info("Could not tag OS volume clone yet, will retry", "volumeID", volume.ID, "err", err.Error())
			return
		}
	}
	conditions.Set(verdaMachine, metav1.Condition{Type: infrav1.OSVolumeReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason})
}

// deleteOSVolume removes the per-machine OS volume clone if it still exists.
// Verda normally deletes the OS volume together with the instance; this covers
// clones whose instance was never created.
func (r *VerdaMachineReconciler) deleteOSVolume(ctx context.Context, verdaMachine *infrav1.VerdaMachine) error {
	id := verdaMachine.Status.OSVolumeID
	if id == "" {
		return nil
	}
	volume, err := r.Cloud.GetVolume(ctx, id)
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			verdaMachine.Status.OSVolumeID = ""
			return nil
		}
		return err
	}
	// Only ever delete the clone we created for this machine.
	if volume.Name != osVolumeName(verdaMachine) {
		ctrl.LoggerFrom(ctx).Info("Recorded OS volume does not match expected clone name, leaving it alone", "volumeID", id, "name", volume.Name)
		verdaMachine.Status.OSVolumeID = ""
		return nil
	}
	if volume.Status != "deleting" && volume.Status != "deleted" {
		ctrl.LoggerFrom(ctx).Info("Deleting OS volume clone", "volumeID", id)
		if err := r.Cloud.DeleteVolume(ctx, id); err != nil {
			return err
		}
	}
	verdaMachine.Status.OSVolumeID = ""
	return nil
}

// osVolumeName is the name of the per-machine OS volume clone. It includes the
// namespace because volume names are global to the Verda account and the same
// machine name can exist in several namespaces.
func osVolumeName(verdaMachine *infrav1.VerdaMachine) string {
	return verdaMachine.Namespace + "-" + verdaMachine.Name + "-os"
}

// osVolumeSize is the OS volume size to request when booting from an image;
// clones keep the size of their source.
func osVolumeSize(verdaMachine *infrav1.VerdaMachine) int32 {
	if verdaMachine.Spec.OSVolumeID != "" {
		return 0
	}
	return verdaMachine.Spec.OSVolumeSizeGB
}

func (r *VerdaMachineReconciler) getBootstrapData(ctx context.Context, machine *clusterv1.Machine) ([]byte, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting bootstrap data secret %s: %w", key, err)
	}
	value, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("bootstrap data secret %s has no \"value\" key", key)
	}
	return value, nil
}

func (r *VerdaMachineReconciler) reconcileDelete(ctx context.Context, verdaMachine *infrav1.VerdaMachine) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	setInstanceReadyFalse(verdaMachine, clusterv1.DeletingReason, "Deleting Verda instance")

	instance, err := r.findInstance(ctx, verdaMachine)
	if err != nil {
		return ctrl.Result{}, err
	}
	if instance != nil && !instanceGone(instance) {
		verdaMachine.Status.InstanceState = instance.Status
		if instance.Status != "deleting" {
			log.Info("Deleting Verda instance", "instanceID", instance.ID)
			if err := r.Cloud.DeleteInstance(ctx, instance.ID); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Verda deletes asynchronously; wait until the instance is gone so the
		// startup script and OS volume are no longer referenced.
		return ctrl.Result{RequeueAfter: deletePollInterval}, nil
	}

	if scriptID := verdaMachine.Status.StartupScriptID; scriptID != "" {
		if err := r.Cloud.DeleteStartupScript(ctx, scriptID); err != nil {
			return ctrl.Result{}, err
		}
		verdaMachine.Status.StartupScriptID = ""
	} else if err := r.Cloud.DeleteStartupScriptByName(ctx, verdaMachine.Name); err != nil {
		// The ID was never recorded (crash between script and instance creation).
		return ctrl.Result{}, err
	}
	if err := r.deleteOSVolume(ctx, verdaMachine); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Verda instance deleted")
	controllerutil.RemoveFinalizer(verdaMachine, infrav1.MachineFinalizer)
	return ctrl.Result{}, nil
}

// instanceGone reports whether an instance no longer exists for our purposes.
// Verda keeps deleted instances queryable with status "discontinued".
func instanceGone(instance *cloud.Instance) bool {
	switch instance.Status {
	case "notfound", "discontinued", "deleted":
		return true
	}
	return false
}

// patchVerdaMachine summarises the Ready condition and persists spec and status.
func patchVerdaMachine(ctx context.Context, patchHelper *patch.Helper, verdaMachine *infrav1.VerdaMachine) error {
	ready := metav1.Condition{Type: clusterv1.ReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason}
	switch {
	case !verdaMachine.DeletionTimestamp.IsZero():
		ready.Status = metav1.ConditionFalse
		ready.Reason = clusterv1.DeletingReason
	case !conditions.IsTrue(verdaMachine, infrav1.InstanceReadyCondition):
		ready.Status = metav1.ConditionFalse
		ready.Reason = clusterv1.NotReadyReason
		ready.Message = conditions.GetMessage(verdaMachine, infrav1.InstanceReadyCondition)
	}
	conditions.Set(verdaMachine, ready)

	return patchHelper.Patch(ctx, verdaMachine, patch.WithOwnedConditions{Conditions: []string{
		clusterv1.PausedCondition,
		clusterv1.ReadyCondition,
		infrav1.InstanceReadyCondition,
		infrav1.OSVolumeReadyCondition,
	}})
}

func setInstanceReadyFalse(verdaMachine *infrav1.VerdaMachine, reason, message string) {
	conditions.Set(verdaMachine, metav1.Condition{
		Type:    infrav1.InstanceReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
}

// machineTagValue is the tag value that ties an instance to its VerdaMachine.
// Verda tag values are limited to 127 characters; namespace/name can exceed
// that, so use a stable digest-free form and truncate only when necessary.
func machineTagValue(verdaMachine *infrav1.VerdaMachine) string {
	return truncateTag(verdaMachine.Namespace + "/" + verdaMachine.Name)
}

// truncateTag lowercases and trims a tag value to Verda's 127 character limit.
func truncateTag(v string) string {
	if len(v) > 127 {
		v = v[:127]
	}
	return strings.ToLower(v)
}

func instanceAddresses(instance *cloud.Instance) []clusterv1.MachineAddress {
	addresses := []clusterv1.MachineAddress{
		{Type: clusterv1.MachineHostName, Address: instance.Hostname},
	}
	if instance.IP != "" {
		// Verda instances currently expose a single public address.
		addresses = append(addresses,
			clusterv1.MachineAddress{Type: clusterv1.MachineExternalIP, Address: instance.IP},
			clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: instance.IP},
		)
	}
	return addresses
}

// SetupWithManager sets up the controller with the Manager.
func (r *VerdaMachineReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "verdamachine")
	clusterToVerdaMachines, err := util.ClusterToTypedObjectsMapper(mgr.GetClient(), &infrav1.VerdaMachineList{}, mgr.GetScheme())
	if err != nil {
		return fmt.Errorf("creating cluster to VerdaMachines mapper: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.VerdaMachine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("VerdaMachine"))),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToVerdaMachines),
			builder.WithPredicates(predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(mgr.GetScheme(), predicateLog)),
		).
		Named("verdamachine").
		Complete(r)
}
