/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "github.com/MKAbuMattar/huawei-sfs-operator/api/v1alpha1"
	"github.com/MKAbuMattar/huawei-sfs-operator/internal/huawei"
)

// ─── fake huawei.Interface ──────────────────────────────────────────
//
// Records every call and lets each test script the responses. Keeps
// the test surface small — we exercise the reconciler's branches,
// not the SDK itself.

type fakeHuawei struct {
	// scripted responses
	createID  string
	createErr error
	getInfo   *huawei.ShareInfo
	getErr    error
	expandErr error

	// the — tag + SG drift fakes
	tags         map[string]string // current "live" tag state
	listTagsErr  error
	addTagErr    error
	deleteTagErr error
	sgChangeErr  error

	// call audit
	createCalls    int
	getCalls       int
	deleteCalls    int
	expandCalls    int
	listTagsCalls  int
	addTagCalls    int
	deleteTagCalls int
	sgChangeCalls  int
	lastCreate     huawei.CreateInput
	lastExpand     int32
	lastSgChange   string
	addedTags      map[string]string // keys+values passed to AddTag
	deletedTags    []string          // keys passed to DeleteTag (in order)
}

func (f *fakeHuawei) Create(_ context.Context, in huawei.CreateInput) (string, error) {
	f.createCalls++
	f.lastCreate = in
	return f.createID, f.createErr
}
func (f *fakeHuawei) Get(_ context.Context, _ string) (*huawei.ShareInfo, error) {
	f.getCalls++
	return f.getInfo, f.getErr
}
func (f *fakeHuawei) Delete(_ context.Context, _ string) error {
	f.deleteCalls++
	return nil
}
func (f *fakeHuawei) FindByName(_ context.Context, _ string) (*huawei.ShareInfo, error) {
	// Note: these tests don't exercise FindByName — the Create-409-adopt
	// path is covered at the SDK-wrapper layer; the reconciler doesn't
	// see it. Returning ErrFsNotFound keeps the interface satisfied
	// and any accidental reliance on it surfaces as a clear failure.
	return nil, huawei.ErrFsNotFound
}
func (f *fakeHuawei) Expand(_ context.Context, _ string, newSize int32) error {
	f.expandCalls++
	f.lastExpand = newSize
	return f.expandErr
}

// the — tag-mgmt + SG fakes.
func (f *fakeHuawei) ListTags(_ context.Context, _ string) (map[string]string, error) {
	f.listTagsCalls++
	if f.listTagsErr != nil {
		return nil, f.listTagsErr
	}
	out := make(map[string]string, len(f.tags))
	maps.Copy(out, f.tags)
	return out, nil
}

func (f *fakeHuawei) AddTag(_ context.Context, _ string, key, value string) error {
	f.addTagCalls++
	if f.addTagErr != nil {
		return f.addTagErr
	}
	if f.tags == nil {
		f.tags = make(map[string]string)
	}
	f.tags[key] = value
	if f.addedTags == nil {
		f.addedTags = make(map[string]string)
	}
	f.addedTags[key] = value
	return nil
}

func (f *fakeHuawei) DeleteTag(_ context.Context, _ string, key string) error {
	f.deleteTagCalls++
	if f.deleteTagErr != nil {
		return f.deleteTagErr
	}
	delete(f.tags, key)
	f.deletedTags = append(f.deletedTags, key)
	return nil
}

func (f *fakeHuawei) ChangeSecurityGroup(_ context.Context, _, newSg string) error {
	f.sgChangeCalls++
	f.lastSgChange = newSg
	return f.sgChangeErr
}

// ─── helpers ────────────────────────────────────────────────────────

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := storagev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add storage scheme: %v", err)
	}
	return s
}

// newCR returns a steady-state CR — finalizer already present, no
// deletion timestamp. Tests that want to exercise the
// finalizer-add or delete branches override these fields.
func newCR() *storagev1alpha1.SfsTurboInstance {
	return &storagev1alpha1.SfsTurboInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "shared-storage",
			Namespace:  "nafith-dev-0-1-2",
			Generation: 1,
			Finalizers: []string{storagev1alpha1.Finalizer},
		},
		Spec: storagev1alpha1.SfsTurboInstanceSpec{
			ReclaimPolicy:     storagev1alpha1.ReclaimPolicyRetain,
			FsName:            "test-sfs-fs-0-1-2",
			Size:              500,
			ShareType:         storagev1alpha1.ShareTypeStandard,
			ShareProtocol:     "NFS",
			AvailabilityZone:  "me-east-1a",
			VpcId:             "vpc-aaa",
			SubnetId:          "sub-bbb",
			AutoCreateSgRules: true,
		},
	}
}

// hasFinalizer reports whether the CR has the controller's finalizer.
func hasFinalizer(cr *storagev1alpha1.SfsTurboInstance) bool {
	return slices.Contains(cr.Finalizers, storagev1alpha1.Finalizer)
}

// runReconcile builds a fake client around cr, runs one Reconcile,
// then returns the CR state afterward. Returns (nil, res, err) when
// the CR was GC'd by finalizer-removal (delete path).
func runReconcile(t *testing.T, cr *storagev1alpha1.SfsTurboInstance, h huawei.Interface) (*storagev1alpha1.SfsTurboInstance, ctrl.Result, error) {
	t.Helper()
	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{
		Client: c,
		Scheme: scheme,
		Huawei: h,
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	})
	out := &storagev1alpha1.SfsTurboInstance{}
	getErr := c.Get(context.Background(), client.ObjectKeyFromObject(cr), out)
	if apierrors.IsNotFound(getErr) {
		// CR was finalizer-cleared and GC'd. Return nil so tests can
		// detect this; reconcile error / result still passed back.
		return nil, res, err
	}
	if getErr != nil {
		t.Fatalf("get post-reconcile: %v", getErr)
	}
	return out, res, err
}

// condition returns the named condition's status, or "" if absent.
func condition(cr *storagev1alpha1.SfsTurboInstance, t string) metav1.ConditionStatus {
	for _, c := range cr.Status.Conditions {
		if c.Type == t {
			return c.Status
		}
	}
	return ""
}

// ─── tests ──────────────────────────────────────────────────────────

func TestReconcile_PausedSkipsHuawei(t *testing.T) {
	cr := newCR()
	cr.Spec.PausedReconcile = true
	h := &fakeHuawei{}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.createCalls != 0 || h.getCalls != 0 {
		t.Fatalf("paused CR should not call Huawei (got create=%d, get=%d)", h.createCalls, h.getCalls)
	}
	if got := condition(out, storagev1alpha1.ConditionProvisioning); got != metav1.ConditionFalse {
		t.Errorf("Provisioning condition: got=%q want=False", got)
	}
}

func TestReconcile_FirstReconcileCallsCreate(t *testing.T) {
	cr := newCR()
	h := &fakeHuawei{createID: "fs-uuid-1"}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.createCalls != 1 {
		t.Fatalf("Create call count: got=%d want=1", h.createCalls)
	}
	if out.Status.FsId != "fs-uuid-1" {
		t.Errorf("status.fsId: got=%q want=fs-uuid-1", out.Status.FsId)
	}
	if got := condition(out, storagev1alpha1.ConditionProvisioning); got != metav1.ConditionTrue {
		t.Errorf("Provisioning condition: got=%q want=True", got)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 after create, got %v", res.RequeueAfter)
	}
	// Spec → CreateInput passthrough
	if h.lastCreate.Name != "test-sfs-fs-0-1-2" {
		t.Errorf("CreateInput.Name: got=%q", h.lastCreate.Name)
	}
	if h.lastCreate.Size != 500 {
		t.Errorf("CreateInput.Size: got=%d want=500", h.lastCreate.Size)
	}
}

func TestReconcile_CreateErrorSetsFailed(t *testing.T) {
	cr := newCR()
	h := &fakeHuawei{createErr: errors.New("boom")}

	out, _, err := runReconcile(t, cr, h)
	if err == nil {
		t.Fatal("expected reconcile error, got nil")
	}
	if got := condition(out, storagev1alpha1.ConditionFailed); got != metav1.ConditionTrue {
		t.Errorf("Failed condition: got=%q want=True", got)
	}
	if out.Status.FsId != "" {
		t.Errorf("status.fsId should be empty on create failure, got=%q", out.Status.FsId)
	}
}

func TestReconcile_PollReadyWritesExport(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-existing"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-existing", Status: "200", ExportLocation: "10.100.11.5:/"},
	}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.getCalls != 1 || h.createCalls != 0 {
		t.Fatalf("expected exactly 1 Get and 0 Create, got get=%d create=%d", h.getCalls, h.createCalls)
	}
	if out.Status.ExportPath != "10.100.11.5:/" {
		t.Errorf("status.exportPath: got=%q", out.Status.ExportPath)
	}
	if got := condition(out, storagev1alpha1.ConditionReady); got != metav1.ConditionTrue {
		t.Errorf("Ready condition: got=%q want=True", got)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("ready: should NOT requeue, got=%v", res.RequeueAfter)
	}
}

func TestReconcile_PollStillCreatingRequeues(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-creating"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-creating", Status: "100"}, // creating
	}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("creating: expected RequeueAfter > 0, got=%v", res.RequeueAfter)
	}
	if out.Status.ExportPath != "" {
		t.Errorf("exportPath should be empty while creating, got=%q", out.Status.ExportPath)
	}
}

func TestReconcile_PollErroredSetsFailed(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-broken"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-broken", Status: "303"}, // create failed
	}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := condition(out, storagev1alpha1.ConditionFailed); got != metav1.ConditionTrue {
		t.Errorf("Failed condition: got=%q want=True", got)
	}
}

func TestReconcile_NotFoundUpstreamClearsFsId(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-gone"
	h := &fakeHuawei{
		getErr: huawei.ErrFsNotFound,
	}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Status.FsId != "" {
		t.Errorf("expected status.fsId cleared, got=%q", out.Status.FsId)
	}
}

func TestReconcile_MissingCRIsNoOp(t *testing.T) {
	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	h := &fakeHuawei{}
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "x"},
	})
	if err != nil {
		t.Fatalf("missing CR should be no-op, got err=%v", err)
	}
	if h.createCalls != 0 || h.getCalls != 0 {
		t.Fatalf("missing CR should not call Huawei")
	}
}

// ─── Finalizer + reclaim policy ────────────────────────────

func TestReconcile_AddsFinalizerWhenMissing(t *testing.T) {
	cr := newCR()
	cr.Finalizers = nil // first-ever reconcile — finalizer not yet attached
	h := &fakeHuawei{createID: "fs-uuid-x"}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Branch 2 should add the finalizer and requeue, BEFORE any
	// Huawei API call. Without this, deleting the CR during the
	// create window would orphan the FS.
	if !hasFinalizer(out) {
		t.Errorf("finalizer not present after first reconcile")
	}
	if h.createCalls != 0 {
		t.Errorf("Huawei.Create should not run before finalizer is persisted, got=%d", h.createCalls)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter>0 after finalizer add, got=%+v", res)
	}
}

func TestReconcile_DeleteWithRetainSkipsHuawei(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-keep"
	cr.Status.ExportPath = "10.0.0.1:/"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyRetain
	h := &fakeHuawei{}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteCalls != 0 {
		t.Errorf("Retain must NOT call Huawei.Delete, got=%d", h.deleteCalls)
	}
	if h.getCalls != 0 {
		t.Errorf("Retain must NOT call Huawei.Get, got=%d", h.getCalls)
	}
	// Finalizer was removed → fake client GC'd the CR.
	if out != nil {
		t.Errorf("CR should be GC'd after finalizer removal; got=%+v", out.Finalizers)
	}
}

func TestReconcile_DeleteWithDeletePolicyIssuesDeleteShare(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed"
	cr.Status.ExportPath = "10.0.0.2:/"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	// FS still exists upstream → first pass should issue DeleteShare.
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-doomed", Status: "200"},
	}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteCalls != 1 {
		t.Errorf("DeleteShare call count: got=%d want=1", h.deleteCalls)
	}
	// Should NOT remove finalizer yet — Delete is in flight.
	if out == nil {
		t.Fatal("CR should still exist while delete is in flight")
	}
	if !hasFinalizer(out) {
		t.Errorf("finalizer should remain while delete in flight")
	}
	if condition(out, storagev1alpha1.ConditionDeleting) != metav1.ConditionTrue {
		t.Errorf("Deleting condition: got=%q want=True", condition(out, storagev1alpha1.ConditionDeleting))
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 after DeleteShare, got=%v", res.RequeueAfter)
	}
}

func TestReconcile_DeletePollSkipsRedundantDeleteShare(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	// Mark Deleting=True to simulate "DeleteShare already issued".
	cr.Status.Conditions = []metav1.Condition{{
		Type: storagev1alpha1.ConditionDeleting, Status: metav1.ConditionTrue,
		Reason: storagev1alpha1.ReasonFsDeletePending, LastTransitionTime: now,
	}}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-doomed", Status: "120"}, // deleting in progress
	}

	_, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteCalls != 0 {
		t.Errorf("DeleteShare must NOT be called again while delete is in flight, got=%d", h.deleteCalls)
	}
	if h.getCalls != 1 {
		t.Errorf("expected exactly 1 Get during delete poll, got=%d", h.getCalls)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 during delete poll")
	}
}

func TestReconcile_DeleteCompletesWhenFsGoneUpstream(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	// Huawei returns ErrFsNotFound — FS already gone.
	h := &fakeHuawei{getErr: huawei.ErrFsNotFound}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteCalls != 0 {
		t.Errorf("Delete must not be called when Get already says 404, got=%d", h.deleteCalls)
	}
	if out != nil {
		t.Errorf("finalizer should be removed when FS is gone upstream; CR still has %+v", out.Finalizers)
	}
}

func TestReconcile_DeleteWithoutFsIdRemovesFinalizer(t *testing.T) {
	cr := newCR()
	// No FsId set — Create never succeeded. Nothing for us to clean up.
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	h := &fakeHuawei{}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteCalls != 0 || h.getCalls != 0 {
		t.Errorf("no FsId → no Huawei calls; got delete=%d get=%d", h.deleteCalls, h.getCalls)
	}
	if out != nil {
		t.Errorf("CR should be GC'd; still has finalizers=%+v", out.Finalizers)
	}
}

func TestReconcile_DeleteHuaweiErrorRetainsFinalizer(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed"
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	h := &fakeHuawei{
		// FS still exists; DeleteShare transient failure.
		getInfo: &huawei.ShareInfo{Id: "fs-doomed", Status: "200"},
	}
	// Inject delete error via wrapper since fakeHuawei.Delete returns nil.
	hWithErr := &fakeHuaweiDeleteErr{fakeHuawei: h, err: errors.New("huawei 500")}

	out, _, err := runReconcile(t, cr, hWithErr)
	if err == nil {
		t.Fatal("expected reconcile error on Huawei.Delete failure")
	}
	if out == nil {
		t.Fatal("CR should NOT be GC'd on Huawei error")
	}
	if !hasFinalizer(out) {
		t.Errorf("finalizer must remain on Huawei error so we retry")
	}
	if condition(out, storagev1alpha1.ConditionFailed) != metav1.ConditionTrue {
		t.Errorf("Failed condition: got=%q want=True", condition(out, storagev1alpha1.ConditionFailed))
	}
}

func TestReconcile_DeleteWithDeletePolicyCascadesPVPVC(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed-with-pvc"
	cr.Spec.PvcName = "shared-storage"
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	// Pre-stage operator-owned PV+PVC.
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-storage"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Gi")},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-storage", Namespace: cr.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-doomed-with-pvc", Status: "200"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr, pv, pvc).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// PVC + PV should both be in Terminating (deletionTimestamp set)
	// or fully gone (fake client behavior depends on finalizers).
	pvcAfter := &corev1.PersistentVolumeClaim{}
	err = c.Get(context.Background(), types.NamespacedName{Name: "shared-storage", Namespace: cr.Namespace}, pvcAfter)
	if err == nil && pvcAfter.DeletionTimestamp.IsZero() {
		t.Errorf("PVC should be terminating or gone; got live: %+v", pvcAfter)
	}
	pvAfter := &corev1.PersistentVolume{}
	err = c.Get(context.Background(), types.NamespacedName{Name: "shared-storage"}, pvAfter)
	if err == nil && pvAfter.DeletionTimestamp.IsZero() {
		t.Errorf("PV should be terminating or gone; got live: %+v", pvAfter)
	}
	// Huawei.Delete must still have been called.
	if h.deleteCalls != 1 {
		t.Errorf("DeleteShare call count: got=%d want=1", h.deleteCalls)
	}
}

func TestReconcile_DeleteWithDeletePolicyNoPvcNameSkipsCascade(t *testing.T) {
	cr := newCR()
	cr.Status.FsId = "fs-doomed-no-pvc"
	// PvcName intentionally empty (e.g. migration-adopted CR — caller
	// manages PV+PVC). Operator must NOT try to delete anything.
	cr.Spec.PvcName = ""
	cr.Spec.ReclaimPolicy = storagev1alpha1.ReclaimPolicyDelete
	now := metav1.NewTime(time.Now())
	cr.DeletionTimestamp = &now
	// Pre-stage a PV+PVC with the same name as a migration would —
	// they MUST stay untouched.
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "untouchable-pv"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Gi")},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "untouchable-pvc", Namespace: cr.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-doomed-no-pvc", Status: "200"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr, pv, pvc).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// PV+PVC must still exist + NOT be terminating.
	pvAfter := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "untouchable-pv"}, pvAfter); err != nil {
		t.Fatalf("PV must NOT be deleted when pvcName empty: %v", err)
	}
	if !pvAfter.DeletionTimestamp.IsZero() {
		t.Errorf("PV must NOT be marked for deletion when pvcName empty")
	}
}

// ─── Field: size-bump triggers API resize ────────────────────────────

func TestReconcile_SizeBumpTriggersExpandShare(t *testing.T) {
	cr := newCR()
	cr.Spec.Size = 1000 // user wants 1000 GiB
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-grow"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{
			Id: "fs-grow", Status: "200", ExportLocation: "10.0.1.1:/",
			Size: 500, // FS currently 500 GiB
		},
	}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.expandCalls != 1 {
		t.Errorf("Expand call count: got=%d want=1", h.expandCalls)
	}
	if h.lastExpand != 1000 {
		t.Errorf("Expand newSize: got=%d want=1000", h.lastExpand)
	}
	if condition(out, storagev1alpha1.ConditionExpanding) != metav1.ConditionTrue {
		t.Errorf("Expanding condition: got=%q want=True", condition(out, storagev1alpha1.ConditionExpanding))
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected requeue while expanding, got=%v", res.RequeueAfter)
	}
}

func TestReconcile_SizeMatchesNoExpand(t *testing.T) {
	cr := newCR()
	cr.Spec.Size = 500
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-steady"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{
			Id: "fs-steady", Status: "200", ExportLocation: "10.0.1.2:/",
			Size: 500,
		},
	}
	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.expandCalls != 0 {
		t.Errorf("Expand must NOT be called when size matches, got=%d", h.expandCalls)
	}
	if condition(out, storagev1alpha1.ConditionExpanding) == metav1.ConditionTrue {
		t.Errorf("Expanding should not be True when sizes match")
	}
}

func TestReconcile_SizeShrinkRefused(t *testing.T) {
	cr := newCR()
	cr.Spec.Size = 500 // user requested smaller than current
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-bigger"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{
			Id: "fs-bigger", Status: "200", ExportLocation: "10.0.1.3:/",
			Size: 1000, // FS already 1000 — user can't shrink to 500
		},
	}
	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.expandCalls != 0 {
		t.Errorf("shrink must NOT call Expand, got=%d", h.expandCalls)
	}
	if condition(out, storagev1alpha1.ConditionFailed) != metav1.ConditionTrue {
		t.Errorf("Failed condition: got=%q want=True (ShrinkRefused)", condition(out, storagev1alpha1.ConditionFailed))
	}
}

func TestReconcile_ExpandCompletionClearsExpandingCondition(t *testing.T) {
	cr := newCR()
	cr.Spec.Size = 1000
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-grown"
	// Pre-set Expanding=True to simulate "operator already kicked the expand"
	cr.Status.Conditions = []metav1.Condition{{
		Type: storagev1alpha1.ConditionExpanding, Status: metav1.ConditionTrue,
		Reason: storagev1alpha1.ReasonFsExpandPending, LastTransitionTime: metav1.Now(),
	}}
	h := &fakeHuawei{
		// FS now reports the new size — expansion complete on Huawei side
		getInfo: &huawei.ShareInfo{
			Id: "fs-grown", Status: "200", ExportLocation: "10.0.1.4:/",
			Size: 1000,
		},
	}
	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.expandCalls != 0 {
		t.Errorf("expand already complete; must not re-call, got=%d", h.expandCalls)
	}
	if condition(out, storagev1alpha1.ConditionExpanding) != metav1.ConditionFalse {
		t.Errorf("Expanding should be False after completion, got=%q", condition(out, storagev1alpha1.ConditionExpanding))
	}
	if out.Status.Size != 1000 {
		t.Errorf("status.size should reflect new size, got=%d", out.Status.Size)
	}
}

// fakeHuaweiDeleteErr lets one test inject an error from Delete()
// without bloating the base fake.
type fakeHuaweiDeleteErr struct {
	*fakeHuawei
	err error
}

func (f *fakeHuaweiDeleteErr) Delete(ctx context.Context, id string) error {
	_ = f.fakeHuawei.Delete(ctx, id) // record the call
	return f.err
}

// ─── Operator-owned PV + PVC ─────────────────────────────

func TestReconcile_PollReadyCreatesPVAndPVC(t *testing.T) {
	cr := newCR()
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-poll-ready"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{
			Id:             "fs-poll-ready",
			Status:         "200",
			ExportLocation: "10.0.0.7:/",
		},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// PV must exist (cluster-scoped).
	var pv corev1.PersistentVolume
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-storage"}, &pv); err != nil {
		t.Fatalf("PV not created: %v", err)
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeHandle != "fs-poll-ready" {
		t.Errorf("PV.csi.volumeHandle: got=%+v want=fs-poll-ready", pv.Spec.CSI)
	}
	if pv.Spec.CSI.VolumeAttributes["everest.io/share-export-location"] != "10.0.0.7:/" {
		t.Errorf("PV export location wrong: got=%q", pv.Spec.CSI.VolumeAttributes["everest.io/share-export-location"])
	}
	if pv.Annotations["argocd.argoproj.io/sync-options"] != "Prune=false" {
		t.Errorf("PV missing Prune=false annotation")
	}

	// PVC must exist (namespaced).
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-storage", Namespace: cr.Namespace}, &pvc); err != nil {
		t.Fatalf("PVC not created: %v", err)
	}
	if pvc.Spec.VolumeName != "shared-storage" {
		t.Errorf("PVC.volumeName: got=%q", pvc.Spec.VolumeName)
	}
	if pvc.Annotations["argocd.argoproj.io/sync-options"] != "Prune=false" {
		t.Errorf("PVC missing Prune=false annotation")
	}
}

func TestReconcile_PollReadyWithoutPvcNameSkipsPVPVC(t *testing.T) {
	cr := newCR()
	// PvcName intentionally empty — caller manages PV+PVC themselves.
	cr.Status.FsId = "fs-no-pvc-name"
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-no-pvc-name", Status: "200", ExportLocation: "10.0.0.8:/"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// No PV+PVC should be created.
	var pvList corev1.PersistentVolumeList
	if err := c.List(context.Background(), &pvList); err != nil {
		t.Fatalf("list PVs: %v", err)
	}
	if len(pvList.Items) != 0 {
		t.Errorf("expected 0 PVs when PvcName empty, got %d", len(pvList.Items))
	}
}

func TestReconcile_PollReadyIdempotentOnPVPVC(t *testing.T) {
	cr := newCR()
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-existing-pv"
	cr.Status.ExportPath = "10.0.0.9:/"
	// Pre-stage an existing PV+PVC.
	existingPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-storage"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Gi")},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
	existingPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-storage", Namespace: cr.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		},
	}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-existing-pv", Status: "200", ExportLocation: "10.0.0.9:/"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr, existingPV, existingPVC).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}

	// Two reconciles back-to-back — neither should error or create
	// duplicates.
	for i := range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		}); err != nil {
			t.Fatalf("reconcile #%d: %v", i, err)
		}
	}

	var pvList corev1.PersistentVolumeList
	if err := c.List(context.Background(), &pvList); err != nil {
		t.Fatal(err)
	}
	if len(pvList.Items) != 1 {
		t.Errorf("expected 1 PV (idempotent), got %d", len(pvList.Items))
	}
}

// ─── Released-PV claimRef recovery on re-deploy ──────────────

// TestReconcile_PollReadyClearsStaleClaimRefOnReleasedPV exercises the
// re-deploy recovery path in ensurePVPVC: when a slot is uninstalled
// (PVC deleted but PV kept via helm.sh/resource-policy + Retain) and
// re-deployed, the operator must clear the PV's stale claimRef so the
// freshly-created PVC can bind. Without this, the new PVC stays
// Pending forever with "FailedBinding: already bound to a different
// claim".
func TestReconcile_PollReadyClearsStaleClaimRefOnReleasedPV(t *testing.T) {
	cr := newCR()
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-stale-claimref"
	cr.Status.ExportPath = "10.0.0.10:/"

	// Pre-stage a Released PV that we own, with a stale claimRef
	// pointing at a UID that no longer exists in-cluster.
	staleUID := types.UID("11111111-2222-3333-4444-555555555555")
	existingPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-storage",
			Labels: map[string]string{
				managedByKey: managedByValue,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Gi")},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			ClaimRef: &corev1.ObjectReference{
				Namespace: cr.Namespace,
				Name:      "shared-storage",
				UID:       staleUID,
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeReleased,
		},
	}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-stale-claimref", Status: "200", ExportLocation: "10.0.0.10:/"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr, existingPV).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}, &corev1.PersistentVolume{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// PV must still exist (operator clears claimRef, does NOT delete).
	var pv corev1.PersistentVolume
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-storage"}, &pv); err != nil {
		t.Fatalf("PV should still exist: %v", err)
	}
	// Stale claimRef must be cleared so the new PVC can bind.
	if pv.Spec.ClaimRef != nil {
		t.Errorf("claimRef should be cleared, got %+v", pv.Spec.ClaimRef)
	}
	// PVC must be created — that's what triggers the re-bind.
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-storage", Namespace: cr.Namespace}, &pvc); err != nil {
		t.Fatalf("PVC should be created: %v", err)
	}
}

// TestReconcile_PollReadyDoesNotTouchReleasedPVOwnedByOther guards the
// scope of the claimRef cleanup: third-party Released PVs (no
// managed-by label, or a foreign managed-by value) must NEVER be
// modified by this operator, no matter what state they're in.
func TestReconcile_PollReadyDoesNotTouchReleasedPVOwnedByOther(t *testing.T) {
	cr := newCR()
	cr.Spec.PvcName = "shared-storage"
	cr.Status.FsId = "fs-foreign-pv"
	cr.Status.ExportPath = "10.0.0.11:/"

	foreignUID := types.UID("99999999-aaaa-bbbb-cccc-ddddddddddde")
	foreignPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shared-storage",
			Labels: map[string]string{
				// Different owner — we MUST NOT touch this PV.
				managedByKey: "some-other-operator",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Gi")},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			ClaimRef: &corev1.ObjectReference{
				Namespace: cr.Namespace,
				Name:      "shared-storage",
				UID:       foreignUID,
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}
	h := &fakeHuawei{
		getInfo: &huawei.ShareInfo{Id: "fs-foreign-pv", Status: "200", ExportLocation: "10.0.0.11:/"},
	}

	scheme := newScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr, foreignPV).
		WithStatusSubresource(&storagev1alpha1.SfsTurboInstance{}, &corev1.PersistentVolume{}).
		Build()
	r := &SfsTurboInstanceReconciler{Client: c, Scheme: scheme, Huawei: h}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
	}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	var pv corev1.PersistentVolume
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-storage"}, &pv); err != nil {
		t.Fatalf("PV should still exist: %v", err)
	}
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != foreignUID {
		t.Errorf("foreign PV's claimRef must NOT be touched; got %+v", pv.Spec.ClaimRef)
	}
}

// ─── Field: spec-drift reconciliation tests ─────────────────────────

// readyShareInfo returns a baseline live FS state that matches newCR()'s
// spec. Helpers nudge individual fields to simulate drift.
func readyShareInfo() *huawei.ShareInfo {
	return &huawei.ShareInfo{
		Id:               "fs-drift-test",
		Status:           "200",
		ExportLocation:   "10.0.1.99:/",
		Size:             500,
		AvailabilityZone: "me-east-1a",
		ShareType:        "STANDARD",
		VpcId:            "vpc-aaa",
		SubnetId:         "sub-bbb",
		SecurityGroupId:  "sg-original",
		CryptKeyId:       "",
		SubStatus:        "",
	}
}

func TestReconcile_TagsDriftAddsMissing(t *testing.T) {
	cr := newCR()
	cr.Spec.Tags = map[string]string{"Environment": "dev", "App": "nafith"}
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	h := &fakeHuawei{getInfo: info, tags: map[string]string{}}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.addTagCalls != 2 {
		t.Errorf("AddTag calls: got=%d want=2", h.addTagCalls)
	}
	if h.addedTags["Environment"] != "dev" || h.addedTags["App"] != "nafith" {
		t.Errorf("AddTag content wrong: %v", h.addedTags)
	}
	if h.deleteTagCalls != 0 {
		t.Errorf("DeleteTag must NOT be called when only adding, got=%d", h.deleteTagCalls)
	}
	if out.Status.Tags["Environment"] != "dev" || out.Status.Tags["App"] != "nafith" {
		t.Errorf("status.Tags not mirrored: %v", out.Status.Tags)
	}
}

func TestReconcile_TagsDriftRemovesExtras(t *testing.T) {
	cr := newCR()
	cr.Spec.Tags = map[string]string{"Environment": "dev"}
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	h := &fakeHuawei{
		getInfo: info,
		tags:    map[string]string{"Environment": "dev", "Stale": "yes", "ToDrop": "x"},
	}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.deleteTagCalls != 2 {
		t.Errorf("DeleteTag calls: got=%d want=2", h.deleteTagCalls)
	}
	gotDeleted := map[string]bool{}
	for _, k := range h.deletedTags {
		gotDeleted[k] = true
	}
	if !gotDeleted["Stale"] || !gotDeleted["ToDrop"] {
		t.Errorf("expected DeleteTag(Stale)+DeleteTag(ToDrop), got=%v", h.deletedTags)
	}
	if h.addTagCalls != 0 {
		t.Errorf("AddTag must NOT be called when only removing, got=%d", h.addTagCalls)
	}
	if _, exists := out.Status.Tags["Stale"]; exists {
		t.Errorf("Stale should have been removed from status.Tags: %v", out.Status.Tags)
	}
}

func TestReconcile_TagsDriftUpdatesMismatch(t *testing.T) {
	cr := newCR()
	cr.Spec.Tags = map[string]string{"Environment": "prod"} // user upgraded
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	h := &fakeHuawei{
		getInfo: info,
		tags:    map[string]string{"Environment": "dev"}, // live still says dev
	}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.addTagCalls != 1 {
		t.Errorf("AddTag (for update) calls: got=%d want=1", h.addTagCalls)
	}
	if h.addedTags["Environment"] != "prod" {
		t.Errorf("AddTag should have updated to prod, got=%v", h.addedTags)
	}
	if out.Status.Tags["Environment"] != "prod" {
		t.Errorf("status.Tags should reflect new value, got=%v", out.Status.Tags)
	}
}

func TestReconcile_TagsNoDriftIsNoOp(t *testing.T) {
	cr := newCR()
	cr.Spec.Tags = map[string]string{"Environment": "dev", "App": "nafith"}
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	h := &fakeHuawei{
		getInfo: info,
		tags:    map[string]string{"Environment": "dev", "App": "nafith"},
	}

	_, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.listTagsCalls != 1 {
		t.Errorf("ListTags should run exactly once per reconcile, got=%d", h.listTagsCalls)
	}
	if h.addTagCalls != 0 || h.deleteTagCalls != 0 {
		t.Errorf("steady-state must be no-op, got add=%d del=%d", h.addTagCalls, h.deleteTagCalls)
	}
}

func TestReconcile_SecurityGroupDriftIssuesChange(t *testing.T) {
	cr := newCR()
	cr.Spec.SecurityGroupId = "sg-new"
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	info.SecurityGroupId = "sg-original" // live still on old SG
	h := &fakeHuawei{getInfo: info, tags: map[string]string{}}

	out, res, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.sgChangeCalls != 1 {
		t.Errorf("ChangeSecurityGroup calls: got=%d want=1", h.sgChangeCalls)
	}
	if h.lastSgChange != "sg-new" {
		t.Errorf("ChangeSecurityGroup target: got=%q want=sg-new", h.lastSgChange)
	}
	if condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup) != metav1.ConditionTrue {
		t.Errorf("UpdatingSecurityGroup condition: got=%q want=True",
			condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup))
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected requeue after SG change, got=%v", res.RequeueAfter)
	}
	// Tag reconcile MUST NOT have run this loop (early return).
	if h.listTagsCalls != 0 {
		t.Errorf("tag reconcile should be skipped during SG change, got=%d ListTags", h.listTagsCalls)
	}
}

func TestReconcile_SecurityGroupChangeInFlightSkipsSecondCall(t *testing.T) {
	cr := newCR()
	cr.Spec.SecurityGroupId = "sg-new"
	cr.Status.FsId = "fs-drift-test"
	info := readyShareInfo()
	info.SecurityGroupId = "sg-original"
	info.SubStatus = huawei.SubStatusSgChanging // 132 — already in flight
	h := &fakeHuawei{getInfo: info, tags: map[string]string{}}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.sgChangeCalls != 0 {
		t.Errorf("must NOT re-issue ChangeSecurityGroup while in flight, got=%d", h.sgChangeCalls)
	}
	if condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup) != metav1.ConditionTrue {
		t.Errorf("UpdatingSecurityGroup condition: got=%q want=True (still in flight)",
			condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup))
	}
}

func TestReconcile_SecurityGroupChangeCompletionClearsCondition(t *testing.T) {
	cr := newCR()
	cr.Spec.SecurityGroupId = "sg-new"
	cr.Status.FsId = "fs-drift-test"
	cr.Status.Conditions = []metav1.Condition{{
		Type:               storagev1alpha1.ConditionUpdatingSecurityGroup,
		Status:             metav1.ConditionTrue,
		Reason:             storagev1alpha1.ReasonSgUpdatePending,
		LastTransitionTime: metav1.Now(),
	}}
	info := readyShareInfo()
	info.SecurityGroupId = "sg-new" // live now matches spec
	info.SubStatus = ""
	h := &fakeHuawei{getInfo: info, tags: map[string]string{}}

	out, _, err := runReconcile(t, cr, h)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h.sgChangeCalls != 0 {
		t.Errorf("must NOT re-call ChangeSecurityGroup after completion, got=%d", h.sgChangeCalls)
	}
	if condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup) != metav1.ConditionFalse {
		t.Errorf("UpdatingSecurityGroup should be False after completion, got=%q",
			condition(out, storagev1alpha1.ConditionUpdatingSecurityGroup))
	}
	if out.Status.SecurityGroupId != "sg-new" {
		t.Errorf("status.SecurityGroupId should mirror live, got=%q", out.Status.SecurityGroupId)
	}
}

func TestReconcile_ImmutableFieldDriftSetsFailed(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(cr *storagev1alpha1.SfsTurboInstance)
		expected string // expected field name in message
	}{
		{"shareType", func(cr *storagev1alpha1.SfsTurboInstance) {
			cr.Spec.ShareType = storagev1alpha1.ShareTypePerformance
		}, "shareType"},
		{"availabilityZone", func(cr *storagev1alpha1.SfsTurboInstance) {
			cr.Spec.AvailabilityZone = "me-east-1b"
		}, "availabilityZone"},
		{"vpcId", func(cr *storagev1alpha1.SfsTurboInstance) {
			cr.Spec.VpcId = "vpc-zzz"
		}, "vpcId"},
		{"subnetId", func(cr *storagev1alpha1.SfsTurboInstance) {
			cr.Spec.SubnetId = "sub-zzz"
		}, "subnetId"},
		{"cryptKeyId", func(cr *storagev1alpha1.SfsTurboInstance) {
			cr.Spec.CryptKeyId = "kms-cmk-new"
		}, "cryptKeyId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newCR()
			cr.Status.FsId = "fs-drift-test"
			tc.mutate(cr)
			info := readyShareInfo() // matches the un-mutated cr
			h := &fakeHuawei{getInfo: info, tags: map[string]string{}}

			out, _, err := runReconcile(t, cr, h)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if condition(out, storagev1alpha1.ConditionFailed) != metav1.ConditionTrue {
				t.Errorf("expected Failed=True on immutable drift, got=%q",
					condition(out, storagev1alpha1.ConditionFailed))
			}
			gotReason := ""
			for _, c := range out.Status.Conditions {
				if c.Type == storagev1alpha1.ConditionFailed {
					gotReason = c.Reason
				}
			}
			if gotReason != storagev1alpha1.ReasonImmutableFieldChanged {
				t.Errorf("Failed reason: got=%q want=%q", gotReason, storagev1alpha1.ReasonImmutableFieldChanged)
			}
		})
	}
}

// Silences unused-import lint when corev1 isn't referenced.
var _ = corev1.Pod{}
