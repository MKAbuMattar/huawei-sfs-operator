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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReclaimPolicy describes the lifecycle action taken when the
// SfsTurboInstance Custom Resource is deleted.
//
// +kubebuilder:validation:Enum=Retain;Delete
type ReclaimPolicy string

const (
	// ReclaimPolicyRetain leaves the underlying HuaweiCloud SFS Turbo
	// file system intact when the CR is deleted. Operator removes its
	// finalizer immediately and lets the Kubernetes garbage collector
	// reap the CR. Manual cleanup (Terraform destroy / Huawei console)
	// is required to actually free the FS.
	ReclaimPolicyRetain ReclaimPolicy = "Retain"

	// ReclaimPolicyDelete calls the HuaweiCloud SFS Turbo DELETE API
	// on the underlying file system before removing the finalizer.
	// Destroys all data. Irreversible. Default is intentionally
	// NOT Delete — opt-in only.
	ReclaimPolicyDelete ReclaimPolicy = "Delete"
)

// ShareType — Huawei SFS Turbo SKU.
//
// +kubebuilder:validation:Enum=STANDARD;PERFORMANCE
type ShareType string

const (
	ShareTypeStandard    ShareType = "STANDARD"
	ShareTypePerformance ShareType = "PERFORMANCE"
)

// SfsTurboInstanceSpec defines the desired state of an SFS Turbo
// file system managed by the operator. Field semantics mirror the
// `huaweicloud_sfs_turbo` Terraform resource so a migration from
// Terraform-state to operator-state is straight-forward.
type SfsTurboInstanceSpec struct {
	// reclaimPolicy controls what happens to the underlying SFS Turbo
	// file system when this CR is deleted.
	//   Retain (default): leave the FS intact; remove finalizer immediately.
	//   Delete:           call DeleteSfsTurbo, wait for completion, then
	//                     remove the finalizer. Destroys all data.
	// +kubebuilder:default=Retain
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// pausedReconcile is a safety switch. When true the controller
	// returns early without touching the CR or the Huawei API. Used
	// for manual operator-side interventions without fighting the
	// reconciler.
	// +kubebuilder:default=false
	// +optional
	PausedReconcile bool `json:"pausedReconcile,omitempty"`

	// fsName is the SFS Turbo file system name shown in the Huawei
	// console. Immutable after first successful reconcile.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=4
	// +kubebuilder:validation:MaxLength=64
	FsName string `json:"fsName"`

	// size in GiB. STANDARD minimum is 500; PERFORMANCE varies.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=500
	// +kubebuilder:validation:Maximum=32768
	Size int32 `json:"size"`

	// shareType — STANDARD (HDD) or PERFORMANCE (SSD-backed).
	// +kubebuilder:default=STANDARD
	// +optional
	ShareType ShareType `json:"shareType,omitempty"`

	// shareProtocol — only NFS is supported for v1alpha1.
	// +kubebuilder:validation:Enum=NFS
	// +kubebuilder:default=NFS
	// +optional
	ShareProtocol string `json:"shareProtocol,omitempty"`

	// enhanced flips the premium tier toggle. Affects pricing.
	// +kubebuilder:default=false
	// +optional
	Enhanced bool `json:"enhanced,omitempty"`

	// availabilityZone — SFS Turbo Standard is single-AZ. The cluster
	// node pool should overlap with this AZ to avoid cross-AZ NFS
	// latency. Immutable.
	// +kubebuilder:validation:Required
	AvailabilityZone string `json:"availabilityZone"`

	// vpcId — the VPC the FS attaches to. Must be the same VPC the
	// CCE cluster runs in. Immutable.
	// +kubebuilder:validation:Required
	VpcId string `json:"vpcId"`

	// subnetId — subnet inside vpcId. Immutable.
	// +kubebuilder:validation:Required
	SubnetId string `json:"subnetId"`

	// securityGroupId — leave empty ("") to have Huawei auto-create a
	// per-FS security group with the right NFS ports. Provide a
	// pre-created SG ID for prod when you want tighter rules.
	// +optional
	SecurityGroupId string `json:"securityGroupId,omitempty"`

	// cryptKeyId — KMS CMK UUID. When set, the FS is encrypted at
	// rest under this key. Empty string disables encryption.
	// +optional
	CryptKeyId string `json:"cryptKeyId,omitempty"`

	// autoCreateSgRules controls whether Huawei opens NFS ports on
	// the auto-created security group. Ignored when securityGroupId
	// is set explicitly.
	// +kubebuilder:default=true
	// +optional
	AutoCreateSgRules bool `json:"autoCreateSgRules,omitempty"`

	// tags propagate to the underlying SFS Turbo resource. Useful for
	// cost allocation and ownership audits.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// ─── PV / PVC creation (operator-owned) ────────────────────────────
	//
	// When set (or via defaults), the operator creates a PersistentVolume
	// and PersistentVolumeClaim alongside the FS so a Pod can mount it
	// directly. This replaces the helm-lookup pattern (which doesn't work
	// under standard ArgoCD repo-server). (refactored in v0.2).
	//
	// All three are optional. When pvcName is empty the operator skips
	// PV+PVC creation entirely (caller renders them separately).

	// pvcName is the K8s name to use for both the PV (cluster-scoped)
	// and the PVC (namespaced, in the CR's namespace). Pod specs
	// referencing `claimName: <pvcName>` will find it. When empty the
	// operator does NOT render PV+PVC at all.
	// +optional
	PvcName string `json:"pvcName,omitempty"`

	// pvcSize is the K8s storage request (e.g. "500Gi"). Defaults to
	// the same Gi as spec.size. Independent from the underlying FS size
	// so a consumer can request a smaller capacity than the FS supports.
	// +optional
	PvcSize string `json:"pvcSize,omitempty"`

	// storageClassName is the K8s StorageClass on both PV + PVC. The PV
	// is static-bound (no dynamic provisioning), so the class just acts
	// as a marker. Defaults to "csi-sfsturbo-perinstance".
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}

// SfsTurboInstanceStatus reports the observed state of the FS as the
// operator sees it after each reconcile.
type SfsTurboInstanceStatus struct {
	// fsId is the HuaweiCloud SFS Turbo file system UUID once the FS
	// has been successfully created. Consumers (helm chart, etc.) read
	// this to populate static PV manifests.
	// +optional
	FsId string `json:"fsId,omitempty"`

	// exportPath is the NFS export string ("<ip>:/") clients mount.
	// Populated alongside fsId.
	// +optional
	ExportPath string `json:"exportPath,omitempty"`

	// size is the FS's currently-allocated capacity in GiB, as
	// reported by Huawei. When spec.size > status.size, the operator
	// issues an ExpandShare; when spec.size < status.size, the
	// operator refuses (SFS Turbo doesn't support shrinkage) and sets
	// a ShrinkRefused condition.
	// +optional
	Size int32 `json:"size,omitempty"`

	// securityGroupId is the SG currently attached to the FS Huawei-
	// side. The reconciler compares this with spec.SecurityGroupId
	// and calls ChangeSecurityGroup when they drift. Empty when the
	// FS uses Huawei's auto-created SG (spec.SecurityGroupId == "").
	// +optional
	SecurityGroupId string `json:"securityGroupId,omitempty"`

	// tags is the observed tag set on the FS Huawei-side, mirrored
	// from ShowSharedTags. Used by `kubectl describe` and to surface
	// drift to operators investigating "why is tag X gone?" The
	// reconciler reconciles spec.tags → live tags on every loop.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// observedGeneration mirrors .metadata.generation that was
	// reconciled. Use to tell whether the controller has seen the
	// latest spec yet.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions report the current state of the resource. Types:
	//   Ready        — the FS exists and is mountable
	//   Provisioning — the operator is calling CreateSfsTurbo
	//   Deleting     — the CR has a deletion timestamp; finalizer in flight
	//   Failed       — most recent reconcile attempt errored
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Common Condition Types for SfsTurboInstance.status.conditions.
const (
	ConditionReady                 = "Ready"
	ConditionProvisioning          = "Provisioning"
	ConditionDeleting              = "Deleting"
	ConditionExpanding             = "Expanding"
	ConditionUpdatingSecurityGroup = "UpdatingSecurityGroup"
	ConditionFailed                = "Failed"
)

// Common Condition Reasons (machine-readable).
const (
	ReasonFsCreated           = "FsCreated"
	ReasonFsCreatePending     = "FsCreatePending"
	ReasonFsCreateFailed      = "FsCreateFailed"
	ReasonFsDeletePending     = "FsDeletePending"
	ReasonFsDeleted           = "FsDeleted"
	ReasonFsDeleteFailed      = "FsDeleteFailed"
	ReasonFsExpandPending     = "FsExpandPending"
	ReasonFsExpanded          = "FsExpanded"
	ReasonShrinkRefused       = "ShrinkRefused"
	ReasonReconcilePaused     = "ReconcilePaused"
	ReasonHuaweiAPIError      = "HuaweiAPIError"
	ReasonValidationFailed    = "ValidationFailed"
	ReasonRetainPolicyApplied = "RetainPolicyApplied"

	// (spec-drift reconciliation).
	ReasonTagsDriftDetected     = "TagsDriftDetected"
	ReasonTagsReconciled        = "TagsReconciled"
	ReasonSgUpdatePending       = "SgUpdatePending"
	ReasonSgUpdated             = "SgUpdated"
	ReasonSgUpdateFailed        = "SgUpdateFailed"
	ReasonImmutableFieldChanged = "ImmutableFieldChanged"
)

// Finalizer string added to every SfsTurboInstance after first
// successful create. Removed only after the reclaim policy is honored.
const Finalizer = "sfs.huaweicloud.com/sfs-turbo-finalizer"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sfsti
// +kubebuilder:printcolumn:name="FS_ID",type=string,JSONPath=`.status.fsId`
// +kubebuilder:printcolumn:name="EXPORT",type=string,JSONPath=`.status.exportPath`
// +kubebuilder:printcolumn:name="SIZE_GIB",type=integer,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="RECLAIM",type=string,JSONPath=`.spec.reclaimPolicy`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// SfsTurboInstance is the Schema for the sfsturboinstances API.
//
// One CR per HuaweiCloud SFS Turbo file system. The operator owns the
// Huawei-side lifecycle (create / update / delete) but does NOT render
// any PV / PVC / CronJob — that stays in the consuming Helm chart,
// which reads .status.fsId / .status.exportPath via `helm lookup`.
type SfsTurboInstance struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of SfsTurboInstance
	// +required
	Spec SfsTurboInstanceSpec `json:"spec"`

	// status defines the observed state of SfsTurboInstance
	// +optional
	Status SfsTurboInstanceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SfsTurboInstanceList contains a list of SfsTurboInstance
type SfsTurboInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SfsTurboInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SfsTurboInstance{}, &SfsTurboInstanceList{})
}
