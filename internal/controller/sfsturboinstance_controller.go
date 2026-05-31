/*
Copyright 2026 The huawei-sfs-operator Authors.

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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "github.com/MKAbuMattar/huawei-sfs-operator/api/v1alpha1"
	"github.com/MKAbuMattar/huawei-sfs-operator/internal/huawei"
	opmetrics "github.com/MKAbuMattar/huawei-sfs-operator/internal/metrics"
)

// defaultStorageClassName is the StorageClass for FS-backed PVs.
// Per cluster convention — kubernetes/storageclass-sfsturbo.yaml.
const defaultStorageClassName = "csi-sfsturbo-perinstance"

// managedByKey + managedByValue identify PVs and PVCs that this operator owns.
// Used by both buildPV/buildPVC (to stamp the label on creation) and by
// ensurePVPVC's re-deploy-recovery branch (to scope the stale-claimRef cleanup
// to PVs we own, never touching third-party Released PVs).
const (
	managedByKey   = "app.kubernetes.io/managed-by"
	managedByValue = "huawei-sfs-operator"
)

// requeueProvisioning is how often we re-check a "creating" FS.
// SFS Turbo Standard typically becomes available in 1-3 minutes.
const requeueProvisioning = 30 * time.Second

// requeueDeleting paces the "is the FS gone yet" poll. Delete is
// faster than create — usually a few seconds — so we poll harder.
const requeueDeleting = 10 * time.Second

// SfsTurboInstanceReconciler reconciles a SfsTurboInstance object.
// Historical note: the first cut was create-only.
type SfsTurboInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Huawei is the SFS Turbo API client. Injected so tests can swap
	// in a fake (see internal/huawei.Interface).
	Huawei huawei.Interface
}

// +kubebuilder:rbac:groups=sfs.huaweicloud.com,resources=sfsturboinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sfs.huaweicloud.com,resources=sfsturboinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sfs.huaweicloud.com,resources=sfsturboinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the entry point. Branches (in order):
//
//  1. DeletionTimestamp set    → reconcileDelete (Retain | Delete)
//  2. Finalizer missing         → add it, requeue
//  3. spec.pausedReconcile      → no-op, condition Paused
//  4. status.fsId == ""         → CreateShare, store id, requeue
//  5. status.fsId != "" && !rdy → ShowShare; if available, write
//     exportPath + Ready=True; if errored,
//     condition Failed=True; otherwise
//     requeue.
//
// Finalizer is added BEFORE any Huawei API call so a `kubectl delete`
// during the create window still respects the reclaim policy.
func (r *SfsTurboInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := logf.FromContext(ctx).WithValues("sfsti", req.NamespacedName)

	// Outcome metric — single deferred increment captures whichever
	// branch returns. Labels:
	//   result=success         err==nil + no requeue
	//   result=error_transient err!=nil (controller-runtime will requeue)
	//   result=requeued        err==nil + Requeue/RequeueAfter set
	defer func() {
		label := "success"
		if retErr != nil {
			label = "error_transient"
		} else if result.Requeue || result.RequeueAfter > 0 {
			label = "requeued"
		}
		opmetrics.ReconcileTotal.WithLabelValues(req.Namespace, label).Inc()
	}()

	var sfsti storagev1alpha1.SfsTurboInstance
	if err := r.Get(ctx, req.NamespacedName, &sfsti); err != nil {
		if apierrors.IsNotFound(err) {
			// CR was deleted and our finalizer (if any) already cleared.
			// Nothing left to do.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Branch 1 — deletion in progress.
	if !sfsti.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &sfsti)
	}

	// Branch 2 — ensure finalizer present BEFORE we touch Huawei.
	// Without this a CR deleted during the create window would orphan
	// the FS (kubelet GCs the CR before the controller sees the
	// deletion timestamp).
	if !controllerutil.ContainsFinalizer(&sfsti, storagev1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&sfsti, storagev1alpha1.Finalizer)
		if err := r.Update(ctx, &sfsti); err != nil {
			return ctrl.Result{}, err
		}
		log.V(1).Info("added finalizer")
		// Requeue immediately — next pass enters the create/poll branch.
		return ctrl.Result{Requeue: true}, nil
	}

	// Branch 3 — paused.
	if sfsti.Spec.PausedReconcile {
		log.V(1).Info("paused; skipping")
		r.setCondition(&sfsti, storagev1alpha1.ConditionProvisioning, metav1.ConditionFalse,
			storagev1alpha1.ReasonReconcilePaused, "spec.pausedReconcile=true")
		return ctrl.Result{}, r.patchStatus(ctx, &sfsti)
	}

	// Branch 4 — no FS yet, create one.
	if sfsti.Status.FsId == "" {
		return r.reconcileCreate(ctx, &sfsti)
	}

	// Branch 5 — FS id known, poll until ready.
	return r.reconcilePoll(ctx, &sfsti)
}

// reconcileDelete runs when DeletionTimestamp is set. Honors the
// CR's spec.reclaimPolicy:
//
//	Retain → remove our finalizer immediately; FS stays in Huawei
//	         (manual cleanup via terraform / console).
//	Delete → call DeleteShare once (Deleting condition tracks "issued"),
//	         then poll Get until ErrFsNotFound, then remove finalizer.
//
// Edge cases:
//   - status.fsId == ""        → no FS exists; remove finalizer.
//   - FS already gone upstream → treat as success; remove finalizer.
//   - Huawei transient error   → return err; controller-runtime
//     rate-limits the retry.
func (r *SfsTurboInstanceReconciler) reconcileDelete(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("fsId", sfsti.Status.FsId, "reclaim", sfsti.Spec.ReclaimPolicy)

	// Nothing to do if our finalizer isn't there (someone removed it
	// out-of-band, or it was never added because the CR was deleted
	// before the first reconcile finished).
	if !controllerutil.ContainsFinalizer(sfsti, storagev1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Nothing to clean up if we never recorded an FS id.
	if sfsti.Status.FsId == "" {
		log.Info("no FS recorded; removing finalizer")
		return r.removeFinalizer(ctx, sfsti)
	}

	// Retain — fastest path. Leave Huawei alone, remove finalizer.
	if sfsti.Spec.ReclaimPolicy != storagev1alpha1.ReclaimPolicyDelete {
		log.Info("reclaim=Retain; removing finalizer without deleting FS")
		r.setCondition(sfsti, storagev1alpha1.ConditionDeleting, metav1.ConditionFalse,
			storagev1alpha1.ReasonRetainPolicyApplied, "reclaimPolicy=Retain; FS preserved")
		if err := r.patchStatus(ctx, sfsti); err != nil {
			return ctrl.Result{}, err
		}
		return r.removeFinalizer(ctx, sfsti)
	}

	// Delete — must ensure DeleteShare was issued, then poll for
	// completion. Get first; if 404, we're done.
	info, err := r.Huawei.Get(ctx, sfsti.Status.FsId)
	if err != nil {
		if errors.Is(err, huawei.ErrFsNotFound) {
			log.Info("FS already gone upstream; removing finalizer")
			r.setCondition(sfsti, storagev1alpha1.ConditionDeleting, metav1.ConditionFalse,
				storagev1alpha1.ReasonFsDeleted, "FS not found upstream; treated as deleted")
			if err := r.patchStatus(ctx, sfsti); err != nil {
				return ctrl.Result{}, err
			}
			return r.removeFinalizer(ctx, sfsti)
		}
		// Transient API error — set Failed condition, return err for
		// controller-runtime exponential backoff.
		r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
			storagev1alpha1.ReasonHuaweiAPIError, err.Error())
		if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
			log.Error(statusErr, "patch status after delete-get failure")
		}
		return ctrl.Result{}, err
	}

	// FS still exists. Has DeleteShare been called yet? The Deleting
	// condition acts as our "delete-in-flight" marker — set after the
	// first DeleteShare succeeds.
	if !hasConditionTrue(sfsti, storagev1alpha1.ConditionDeleting) {
		// Cascade-delete PV+PVC FIRST so consumer pods get an
		// unambiguous signal that the storage is going away. If pods
		// still hold the PVC, pvc-protection finalizer keeps it in
		// Terminating until they drain — that's correct (the user
		// asked for reclaim=Delete on a live workload, K8s holds
		// them honest). We continue to DeleteShare regardless; the
		// FS data is going either way.
		//
		// Idempotent: 404 on either Get is a successful no-op. Skipped
		// when spec.pvcName is empty (caller manages PV+PVC, e.g.
		// migration CRs).
		if sfsti.Spec.PvcName != "" {
			if err := r.cascadeDeletePVPVC(ctx, sfsti); err != nil {
				log.Error(err, "cascadeDeletePVPVC failed; continuing with FS delete anyway")
				// NOT a hard fail — orphan PV+PVC are recoverable by
				// hand. Hard-failing here would block FS reclaim,
				// which is the user's primary intent.
			}
		}
		log.Info("issuing DeleteShare", "currentStatus", info.Status)
		if err := r.Huawei.Delete(ctx, sfsti.Status.FsId); err != nil {
			r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
				storagev1alpha1.ReasonFsDeleteFailed, err.Error())
			if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
				log.Error(statusErr, "patch status after delete-call failure")
			}
			return ctrl.Result{}, err
		}
		r.setCondition(sfsti, storagev1alpha1.ConditionDeleting, metav1.ConditionTrue,
			storagev1alpha1.ReasonFsDeletePending, "DeleteShare accepted; polling for removal")
		r.setCondition(sfsti, storagev1alpha1.ConditionReady, metav1.ConditionFalse,
			storagev1alpha1.ReasonFsDeletePending, "FS deletion in progress")
		if err := r.patchStatus(ctx, sfsti); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Delete already in flight — just requeue and check again.
	log.V(1).Info("delete in flight; requeueing", "currentStatus", info.Status)
	return ctrl.Result{RequeueAfter: requeueDeleting}, nil
}

// removeFinalizer strips Finalizer and persists the CR. After this
// returns successfully the kubelet GCs the object on the next pass.
func (r *SfsTurboInstanceReconciler) removeFinalizer(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(sfsti, storagev1alpha1.Finalizer)
	if err := r.Update(ctx, sfsti); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// intStr formats an int32 as decimal — used in condition messages
// where fmt.Sprintf would pull in an extra import for one call site.
func intStr(n int32) string {
	return fmt.Sprintf("%d", n)
}

// hasConditionTrue reports whether the named condition exists AND is True.
func hasConditionTrue(sfsti *storagev1alpha1.SfsTurboInstance, t string) bool {
	for _, c := range sfsti.Status.Conditions {
		if c.Type == t && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// reconcileCreate calls CreateShare and stores the returned id.
// Single shot — re-reconciles do NOT re-enter this branch because
// status.fsId is set immediately on success.
func (r *SfsTurboInstanceReconciler) reconcileCreate(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	in := huawei.CreateInput{
		Name:              sfsti.Spec.FsName,
		Size:              sfsti.Spec.Size,
		ShareType:         string(sfsti.Spec.ShareType),
		ShareProto:        sfsti.Spec.ShareProtocol,
		AvailabilityZone:  sfsti.Spec.AvailabilityZone,
		VpcId:             sfsti.Spec.VpcId,
		SubnetId:          sfsti.Spec.SubnetId,
		SecurityGroupId:   sfsti.Spec.SecurityGroupId,
		CryptKeyId:        sfsti.Spec.CryptKeyId,
		AutoCreateSgRules: sfsti.Spec.AutoCreateSgRules,
		Tags:              sfsti.Spec.Tags,
	}

	log.Info("creating SFS Turbo FS", "name", in.Name, "size", in.Size, "az", in.AvailabilityZone)
	id, err := r.Huawei.Create(ctx, in)
	if err != nil {
		r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
			storagev1alpha1.ReasonFsCreateFailed, err.Error())
		if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
			log.Error(statusErr, "patch status after create failure")
		}
		// Requeue with backoff handled by the controller-runtime
		// rate limiter — return err and the framework rate-limits us.
		return ctrl.Result{}, err
	}

	sfsti.Status.FsId = id
	sfsti.Status.ObservedGeneration = sfsti.Generation
	r.setCondition(sfsti, storagev1alpha1.ConditionProvisioning, metav1.ConditionTrue,
		storagev1alpha1.ReasonFsCreatePending, "CreateShare accepted; polling for ready")
	r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionFalse,
		storagev1alpha1.ReasonFsCreatePending, "previous error cleared")

	if err := r.patchStatus(ctx, sfsti); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("FS create accepted; will poll", "fsId", id)
	return ctrl.Result{RequeueAfter: requeueProvisioning}, nil
}

// reconcilePoll calls ShowShare to check whether the FS finished
// creating. On status=available, writes ExportPath + Ready=True; on
// status=errored, sets Failed=True. Otherwise requeues.
func (r *SfsTurboInstanceReconciler) reconcilePoll(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("fsId", sfsti.Status.FsId)

	info, err := r.Huawei.Get(ctx, sfsti.Status.FsId)
	if err != nil {
		if errors.Is(err, huawei.ErrFsNotFound) {
			// FS was destroyed out-of-band. Clear status so the next
			// reconcile re-enters create. Do NOT lose the CR.
			log.Info("FS not found upstream; clearing status to trigger recreate")
			sfsti.Status.FsId = ""
			sfsti.Status.ExportPath = ""
			r.setCondition(sfsti, storagev1alpha1.ConditionReady, metav1.ConditionFalse,
				storagev1alpha1.ReasonHuaweiAPIError, "FS not found upstream; will recreate")
			return ctrl.Result{Requeue: true}, r.patchStatus(ctx, sfsti)
		}
		r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
			storagev1alpha1.ReasonHuaweiAPIError, err.Error())
		if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
			log.Error(statusErr, "patch status after poll failure")
		}
		return ctrl.Result{}, err
	}

	switch {
	case huawei.IsAvailable(info.Status):
		sfsti.Status.ExportPath = info.ExportLocation
		sfsti.Status.Size = info.Size
		sfsti.Status.ObservedGeneration = sfsti.Generation
		r.setCondition(sfsti, storagev1alpha1.ConditionReady, metav1.ConditionTrue,
			storagev1alpha1.ReasonFsCreated, "FS available at "+info.ExportLocation)
		r.setCondition(sfsti, storagev1alpha1.ConditionProvisioning, metav1.ConditionFalse,
			storagev1alpha1.ReasonFsCreated, "create complete")
		r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionFalse,
			storagev1alpha1.ReasonFsCreated, "")
		log.Info("FS ready", "export", info.ExportLocation, "size", info.Size)

		// Field: resize handling. SFS Turbo supports ExpandShare for
		// growth only (no shrinkage). When spec.size differs from
		// the observed status.size, take action — but ONLY when the
		// FS is in the "available" status (200). Mid-expansion the
		// Huawei status would be 121 and we'd be in the default case
		// (poll branch) anyway.
		if info.Size > 0 {
			switch {
			case sfsti.Spec.Size > info.Size:
				// Grow. Mark Expanding=True, call ExpandShare,
				// requeue. Next poll reconciles will tick along
				// while Huawei status is 121; eventually it returns
				// to 200 with the new size and this branch sees
				// spec.size == status.size and clears Expanding.
				if !hasConditionTrue(sfsti, storagev1alpha1.ConditionExpanding) {
					log.Info("issuing ExpandShare", "from", info.Size, "to", sfsti.Spec.Size)
					if err := r.Huawei.Expand(ctx, sfsti.Status.FsId, sfsti.Spec.Size); err != nil {
						r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
							storagev1alpha1.ReasonHuaweiAPIError, err.Error())
						if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
							log.Error(statusErr, "patch status after expand failure")
						}
						return ctrl.Result{}, err
					}
					r.setCondition(sfsti, storagev1alpha1.ConditionExpanding, metav1.ConditionTrue,
						storagev1alpha1.ReasonFsExpandPending,
						"ExpandShare accepted; growing from "+intStr(info.Size)+" GiB to "+intStr(sfsti.Spec.Size)+" GiB")
				}
				if err := r.patchStatus(ctx, sfsti); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: requeueProvisioning}, nil

			case sfsti.Spec.Size < info.Size:
				// Shrink refused — SFS Turbo doesn't support it.
				// Set a clear condition so users see the rejection;
				// don't loop on it.
				r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
					storagev1alpha1.ReasonShrinkRefused,
					"SFS Turbo doesn't support shrinking; spec.size="+intStr(sfsti.Spec.Size)+
						" < status.size="+intStr(info.Size)+". Recreate the slot if you need a smaller FS.")

			default: // spec.size == status.size
				// If we were expanding, this is the completion edge.
				if hasConditionTrue(sfsti, storagev1alpha1.ConditionExpanding) {
					log.Info("FS expansion complete", "size", info.Size)
					r.setCondition(sfsti, storagev1alpha1.ConditionExpanding, metav1.ConditionFalse,
						storagev1alpha1.ReasonFsExpanded, "FS now "+intStr(info.Size)+" GiB")
				}
			}
		}

		// Field: spec-drift reconciliation. Runs only when the FS is in
		// the "available" status — mid-create / mid-expand we're already
		// in a different branch above.
		//
		// (a) Immutable-field guard. ShareType / AvailabilityZone /
		// VpcId / SubnetId / CryptKeyId can't be changed on a live FS
		// (Huawei has no API for it; the FS would need delete+recreate
		// to take a new value). If the user mutated one of these on the
		// CR, surface Failed=True with reason ImmutableFieldChanged —
		// they have to delete + recreate the CR to actually change it.
		if drifted := immutableDrift(sfsti, info); drifted != "" {
			log.Info("immutable field drift detected", "field", drifted)
			r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
				storagev1alpha1.ReasonImmutableFieldChanged,
				"spec field "+drifted+" cannot be changed on a live FS; delete + recreate the CR to take effect")
			// Don't early-return — we still want PV+PVC reconcile to
			// run and other drift to be reported. The Failed condition
			// is informational.
		}

		// (b) Security-group drift. Only acted on when the user pinned
		// a specific SG (spec.SecurityGroupId != ""). When the FS uses
		// Huawei's auto-created SG, spec.SecurityGroupId is empty and
		// we don't touch it.
		if sfsti.Spec.SecurityGroupId != "" && sfsti.Spec.SecurityGroupId != info.SecurityGroupId {
			if huawei.IsSgChangeInFlight(info.SubStatus) {
				// Already in flight (subStatus=132). Make sure the
				// condition reflects that and requeue.
				r.setCondition(sfsti, storagev1alpha1.ConditionUpdatingSecurityGroup,
					metav1.ConditionTrue, storagev1alpha1.ReasonSgUpdatePending,
					"ChangeSecurityGroup in flight (subStatus="+info.SubStatus+")")
			} else {
				log.Info("issuing ChangeSecurityGroup",
					"from", info.SecurityGroupId, "to", sfsti.Spec.SecurityGroupId)
				if err := r.Huawei.ChangeSecurityGroup(ctx, sfsti.Status.FsId, sfsti.Spec.SecurityGroupId); err != nil {
					r.setCondition(sfsti, storagev1alpha1.ConditionUpdatingSecurityGroup,
						metav1.ConditionFalse, storagev1alpha1.ReasonSgUpdateFailed, err.Error())
					if statusErr := r.patchStatus(ctx, sfsti); statusErr != nil {
						log.Error(statusErr, "patch status after SG change failure")
					}
					return ctrl.Result{}, err
				}
				r.setCondition(sfsti, storagev1alpha1.ConditionUpdatingSecurityGroup,
					metav1.ConditionTrue, storagev1alpha1.ReasonSgUpdatePending,
					"ChangeSecurityGroup accepted; switching to "+sfsti.Spec.SecurityGroupId)
			}
			sfsti.Status.SecurityGroupId = info.SecurityGroupId
			if err := r.patchStatus(ctx, sfsti); err != nil {
				return ctrl.Result{}, err
			}
			// SG mutation puts the FS into subStatus=132. Skip tag
			// reconciliation this loop — apply on the next reconcile
			// when subStatus clears.
			return ctrl.Result{RequeueAfter: requeueProvisioning}, nil
		}
		// No SG drift. Sync observed value into status and clear any
		// stale UpdatingSecurityGroup condition.
		sfsti.Status.SecurityGroupId = info.SecurityGroupId
		if hasConditionTrue(sfsti, storagev1alpha1.ConditionUpdatingSecurityGroup) {
			log.Info("SG change complete", "sg", info.SecurityGroupId)
			r.setCondition(sfsti, storagev1alpha1.ConditionUpdatingSecurityGroup,
				metav1.ConditionFalse, storagev1alpha1.ReasonSgUpdated,
				"FS now uses SG "+info.SecurityGroupId)
		}

		// (c) Tag drift. Read live tags, diff against spec, apply all
		// changes (add missing + delete extras + update mismatched).
		// Skip when SG is mid-mutation — Huawei serializes some
		// operations and may reject concurrent mutations.
		if !huawei.IsSgChangeInFlight(info.SubStatus) {
			if err := r.reconcileTagsDrift(ctx, sfsti, log); err != nil {
				log.Error(err, "reconcileTagsDrift failed")
				// Surface but don't block PV+PVC — tag drift is not
				// a mount-blocker.
				r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
					storagev1alpha1.ReasonHuaweiAPIError, "tag reconcile: "+err.Error())
			}
		}

		if err := r.patchStatus(ctx, sfsti); err != nil {
			return ctrl.Result{}, err
		}
		// Operator owns PV+PVC creation when spec.pvcName is set. helm `lookup()`
		// doesn't work under standard ArgoCD repo-server (no cluster
		// access), so the consumer chart can't render PV+PVC from the
		// CR's status. The operator does it instead.
		if err := r.ensurePVPVC(ctx, sfsti); err != nil {
			log.Error(err, "ensurePVPVC failed")
			return ctrl.Result{RequeueAfter: requeueProvisioning}, err
		}
		return ctrl.Result{}, nil

	case huawei.IsErrored(info.Status):
		r.setCondition(sfsti, storagev1alpha1.ConditionFailed, metav1.ConditionTrue,
			storagev1alpha1.ReasonFsCreateFailed, "Huawei reports share status="+info.Status)
		r.setCondition(sfsti, storagev1alpha1.ConditionProvisioning, metav1.ConditionFalse,
			storagev1alpha1.ReasonFsCreateFailed, "")
		log.Error(nil, "FS create failed upstream", "status", info.Status)
		return ctrl.Result{}, r.patchStatus(ctx, sfsti)

	default:
		// Still creating — typical first 1-3 minutes after CreateShare.
		log.V(1).Info("FS not ready yet, requeueing", "status", info.Status)
		return ctrl.Result{RequeueAfter: requeueProvisioning}, nil
	}
}

// immutableDrift returns the spec field that differs from live state
// AND that Huawei doesn't allow mutating on an existing FS, or ""
// when no drift is detected. Used by the to surface unfixable spec
// changes with reason ImmutableFieldChanged.
//
// Comparison rules:
//   - empty values in info are ignored (the SDK may omit fields we
//     happen to read for some FS states)
//   - empty values in spec are NOT ignored — an empty cryptKeyId
//     against an encrypted live FS is still drift
//   - shareProtocol is implicitly compared via the FS's existence
//     (NFS is the only supported value in v1alpha1)
func immutableDrift(sfsti *storagev1alpha1.SfsTurboInstance, info *huawei.ShareInfo) string {
	if info.ShareType != "" && string(sfsti.Spec.ShareType) != "" && string(sfsti.Spec.ShareType) != info.ShareType {
		return "shareType"
	}
	if info.AvailabilityZone != "" && sfsti.Spec.AvailabilityZone != info.AvailabilityZone {
		return "availabilityZone"
	}
	if info.VpcId != "" && sfsti.Spec.VpcId != info.VpcId {
		return "vpcId"
	}
	if info.SubnetId != "" && sfsti.Spec.SubnetId != info.SubnetId {
		return "subnetId"
	}
	// cryptKeyId is one-way: once encrypted, you can't disable
	// encryption (spec="" against info!=""), and you can't swap CMKs.
	if sfsti.Spec.CryptKeyId != info.CryptKeyId {
		return "cryptKeyId"
	}
	return ""
}

// reconcileTagsDrift makes the FS's Huawei-side tag set match
// spec.Tags. Adds missing, deletes extras, updates value mismatches.
// Writes the observed tag set back to status.Tags.
//
// Apply-all-at-once policy: every tag op runs in one reconcile loop,
// no per-tag requeue. The total work is bounded — operator CRs carry
// 3-6 tags typically.
//
// Errors from individual AddTag / DeleteTag calls fail the whole
// reconcile (caller sets Failed=True). Idempotent on retry because
// the next loop re-reads live state and only acts on remaining drift.
func (r *SfsTurboInstanceReconciler) reconcileTagsDrift(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance, log logr.Logger) error {
	live, err := r.Huawei.ListTags(ctx, sfsti.Status.FsId)
	if err != nil {
		return fmt.Errorf("ListTags: %w", err)
	}

	desired := sfsti.Spec.Tags // may be nil; treated as "no tags"
	changed := false

	// Add missing + update value mismatches.
	for k, v := range desired {
		if liveVal, ok := live[k]; !ok || liveVal != v {
			log.Info("tag add/update", "key", k, "value", v)
			if err := r.Huawei.AddTag(ctx, sfsti.Status.FsId, k, v); err != nil {
				return fmt.Errorf("AddTag(%s=%s): %w", k, v, err)
			}
			live[k] = v
			changed = true
		}
	}
	// Remove extras (in live but not in desired). The reconciler is
	// authoritative — if a user removed a tag from spec, we honor
	// that. Out-of-band tags get pruned. (Future: opt-in flag to
	// preserve out-of-band tags if needed.)
	for k := range live {
		if _, ok := desired[k]; !ok {
			log.Info("tag delete", "key", k)
			if err := r.Huawei.DeleteTag(ctx, sfsti.Status.FsId, k); err != nil {
				return fmt.Errorf("DeleteTag(%s): %w", k, err)
			}
			delete(live, k)
			changed = true
		}
	}

	if changed {
		r.setCondition(sfsti, storagev1alpha1.ConditionReady, metav1.ConditionTrue,
			storagev1alpha1.ReasonTagsReconciled, "tags reconciled to spec")
	}
	// Mirror the (now-aligned) live set into status for visibility in
	// `kubectl describe`.
	sfsti.Status.Tags = live
	return nil
}

// setCondition upserts a condition on the CR's status by type. Keeps
// LastTransitionTime monotonic: only advances when status flips.
func (r *SfsTurboInstanceReconciler) setCondition(sfsti *storagev1alpha1.SfsTurboInstance, t string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.NewTime(time.Now().UTC())
	for i := range sfsti.Status.Conditions {
		if sfsti.Status.Conditions[i].Type == t {
			cur := &sfsti.Status.Conditions[i]
			if cur.Status != status {
				cur.LastTransitionTime = now
			}
			cur.Status = status
			cur.Reason = reason
			cur.Message = message
			cur.ObservedGeneration = sfsti.Generation
			return
		}
	}
	sfsti.Status.Conditions = append(sfsti.Status.Conditions, metav1.Condition{
		Type:               t,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: sfsti.Generation,
	})
}

// patchStatus persists status changes via a status subresource patch.
// Returns nil if the CR was deleted in-flight (NotFound).
func (r *SfsTurboInstanceReconciler) patchStatus(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) error {
	err := r.Status().Update(ctx, sfsti)
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// ensurePVPVC reconciles the PersistentVolume + PersistentVolumeClaim
// pair that fronts the SFS Turbo FS. Idempotent — Get first, Create
// if missing, leave alone if present. We deliberately do NOT update
// drifted PV/PVC fields in this phase; immutable fields would error
// and mutable ones (capacity, mountOptions) don't typically drift.
//
// Skipped when spec.pvcName is empty — caller is responsible for
// rendering PV+PVC separately (legacy slots, or special cases like
// multi-PVC-per-FS).
func (r *SfsTurboInstanceReconciler) ensurePVPVC(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) error {
	log := logf.FromContext(ctx)
	if sfsti.Spec.PvcName == "" {
		log.V(1).Info("spec.pvcName empty; skipping PV+PVC creation (caller manages)")
		return nil
	}
	if sfsti.Status.FsId == "" || sfsti.Status.ExportPath == "" {
		// Defensive — this method is only called after the Ready
		// transition, but guard against accidental future callers.
		return errors.New("ensurePVPVC called before status populated")
	}

	storageClass := sfsti.Spec.StorageClassName
	if storageClass == "" {
		storageClass = defaultStorageClassName
	}

	// Capacity. The operator allocates the K8s "request" capacity
	// independently from the FS's actual size — typically equal, but
	// the caller could request smaller.
	pvcSize := sfsti.Spec.PvcSize
	if pvcSize == "" {
		pvcSize = (resource.NewQuantity(int64(sfsti.Spec.Size)*1024*1024*1024, resource.BinarySI)).String()
	}
	quantity, err := resource.ParseQuantity(pvcSize)
	if err != nil {
		return errors.New("invalid pvcSize: " + pvcSize + ": " + err.Error())
	}

	// ─── PersistentVolume (cluster-scoped) ────────────────────────────
	pv := &corev1.PersistentVolume{}
	pvKey := types.NamespacedName{Name: sfsti.Spec.PvcName}
	switch err := r.Get(ctx, pvKey, pv); {
	case apierrors.IsNotFound(err):
		pv = r.buildPV(sfsti, storageClass, quantity)
		if err := r.Create(ctx, pv); err != nil {
			return err
		}
		log.Info("created PV", "name", pv.Name)
	case err != nil:
		return err
	default:
		// Re-deploy recovery: a PV that we previously owned can be left
		// in `Released` state when the slot is uninstalled and then
		// re-deployed. The PVC is gone, but Helm's
		// `helm.sh/resource-policy: keep` annotation + the PV's `Retain`
		// reclaim policy preserved the PV. Its `spec.claimRef.uid` now
		// points at the *deleted* PVC, so the freshly-created PVC
		// (different UID, same name) can't bind:
		//   FailedBinding: volume "<name>" already bound to a different
		//   claim "<ns>/<name>".
		//
		// We're the source of truth for the PV+PVC lifecycle (the PV
		// carries our `app.kubernetes.io/managed-by` label), so on every
		// reconcile we proactively clear a stale `claimRef` on a
		// Released PV with our ownership label. The new PVC then binds
		// on the very next scheduler loop without any manual kubectl
		// intervention.
		//
		// Guarded by THREE preconditions to avoid touching anything we
		// don't own or that's currently in-flight:
		//   1. Phase must be `Released`            (Pending/Bound/Available untouched)
		//   2. managed-by label must match ours    (third-party PVs untouched)
		//   3. claimRef must be non-nil            (Available-with-no-claim untouched)
		//
		// Observed live 2026-05-13 on `dev-0-1-0-shared-storage` after
		// the slot was retired + re-deployed by Jenkins. See plan
		// 
		if pv.Status.Phase == corev1.VolumeReleased &&
			pv.Labels[managedByKey] == managedByValue &&
			pv.Spec.ClaimRef != nil {
			staleUID := pv.Spec.ClaimRef.UID
			pv.Spec.ClaimRef = nil
			if err := r.Update(ctx, pv); err != nil {
				return fmt.Errorf("clearing stale claimRef on Released PV %s: %w", pv.Name, err)
			}
			log.Info("cleared stale claimRef on Released PV",
				"name", pv.Name,
				"stale_claimref_uid", staleUID)
		} else {
			log.V(1).Info("PV already exists; skipping create", "name", pv.Name)
		}
	}

	// ─── PersistentVolumeClaim (namespaced) ───────────────────────────
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := types.NamespacedName{Name: sfsti.Spec.PvcName, Namespace: sfsti.Namespace}
	switch err := r.Get(ctx, pvcKey, pvc); {
	case apierrors.IsNotFound(err):
		pvc = r.buildPVC(sfsti, storageClass, quantity)
		if err := r.Create(ctx, pvc); err != nil {
			return err
		}
		log.Info("created PVC", "namespace", pvc.Namespace, "name", pvc.Name)
	case err != nil:
		return err
	default:
		log.V(1).Info("PVC already exists; skipping create", "name", pvc.Name)
	}

	return nil
}

// buildPV constructs a static-bound PV pointing at the SFS Turbo FS.
// reclaimPolicy is always Retain — the FS's lifecycle is driven by
// the CR's reclaimPolicy, NOT the PV's. The Prune=false annotation
// keeps ArgoCD from auto-deleting if the consumer chart's manifest
// list ever drifts.
func (r *SfsTurboInstanceReconciler) buildPV(sfsti *storagev1alpha1.SfsTurboInstance, storageClass string, capacity resource.Quantity) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: sfsti.Spec.PvcName,
			Labels: map[string]string{
				"app.kubernetes.io/component": "shared-storage",
				"app.kubernetes.io/instance":  sfsti.Spec.PvcName,
				managedByKey:                  managedByValue,
				"sfs.huaweicloud.com/owned-by": sfsti.Namespace + "." + sfsti.Name,
			},
			Annotations: map[string]string{
				// Defense-in-depth against historical edge cases.
				"argocd.argoproj.io/sync-options":     "Prune=false",
				"sfs.huaweicloud.com/sfsturboinstance": sfsti.Namespace + "/" + sfsti.Name,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: capacity,
			},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              storageClass,
			ClaimRef: &corev1.ObjectReference{
				Namespace: sfsti.Namespace,
				Name:      sfsti.Spec.PvcName,
			},
			MountOptions: []string{"vers=3", "nolock", "noresvport", "timeo=600"},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "sfsturbo.csi.everest.io",
					FSType:       "nfs",
					VolumeHandle: sfsti.Status.FsId,
					VolumeAttributes: map[string]string{
						"everest.io/share-export-location":             sfsti.Status.ExportPath,
						"everest.io/share-source":                      "sfs-turbo",
						"storage.kubernetes.io/csiProvisionerIdentity": "everest-csi-provisioner",
					},
				},
			},
		},
	}
}

func (r *SfsTurboInstanceReconciler) buildPVC(sfsti *storagev1alpha1.SfsTurboInstance, storageClass string, capacity resource.Quantity) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sfsti.Spec.PvcName,
			Namespace: sfsti.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component": "shared-storage",
				"app.kubernetes.io/instance":  sfsti.Spec.PvcName,
				managedByKey:                  managedByValue,
				"sfs.huaweicloud.com/owned-by": sfsti.Namespace + "." + sfsti.Name,
			},
			Annotations: map[string]string{
				"helm.sh/resource-policy":             "keep",
				"argocd.argoproj.io/sync-options":     "Prune=false",
				"sfs.huaweicloud.com/sfsturboinstance": sfsti.Namespace + "/" + sfsti.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			StorageClassName: &storageClass,
			VolumeName:       sfsti.Spec.PvcName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: capacity,
				},
			},
		},
	}
}

// cascadeDeletePVPVC deletes the operator-owned PVC + PV for a CR
// that's being torn down with reclaimPolicy=Delete. Mirrors
// ensurePVPVC's idempotency contract — NotFound is a successful
// no-op; any other error bubbles up.
//
// Order matters: PVC first (so kube-controller-manager removes the
// pv-protection finalizer when the PV's claimRef.uid no longer
// matches), then PV. If pods still hold the PVC, the pvc-protection
// finalizer keeps it Terminating; the caller chooses to proceed with
// Huawei.Delete anyway (best-effort cleanup).
func (r *SfsTurboInstanceReconciler) cascadeDeletePVPVC(ctx context.Context, sfsti *storagev1alpha1.SfsTurboInstance) error {
	log := logf.FromContext(ctx)

	// ─── PVC (namespaced) ─────────────────────────────────────────────
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := types.NamespacedName{Name: sfsti.Spec.PvcName, Namespace: sfsti.Namespace}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		log.V(1).Info("PVC already absent; skipping delete", "name", sfsti.Spec.PvcName)
	} else if pvc.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		log.Info("issued PVC delete", "namespace", pvc.Namespace, "name", pvc.Name)
	} else {
		log.V(1).Info("PVC already terminating; skipping re-delete", "name", pvc.Name)
	}

	// ─── PV (cluster-scoped) ──────────────────────────────────────────
	pv := &corev1.PersistentVolume{}
	pvKey := types.NamespacedName{Name: sfsti.Spec.PvcName}
	if err := r.Get(ctx, pvKey, pv); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		log.V(1).Info("PV already absent; skipping delete", "name", sfsti.Spec.PvcName)
		return nil
	}
	if pv.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pv); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		log.Info("issued PV delete", "name", pv.Name)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SfsTurboInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.SfsTurboInstance{}).
		Named("sfsturboinstance").
		Complete(r)
}
