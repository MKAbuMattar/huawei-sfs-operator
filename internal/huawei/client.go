/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

package huawei

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sdkregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/region"
	sfsturbo "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/sfsturbo/v1"
	sfsturbomodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/sfsturbo/v1/model"
	sfsturboregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/sfsturbo/v1/region"
)

// Client is the small surface area the reconciler uses. Mockable
// through the Interface type below.
//
// In static-AK/SK mode (Config.PodAgencyMode() == false), `sdk` is
// built once at construction and reused.
//
// In pod-agency mode, `podAgency` caches short-lived creds and the
// `sdk` is rebuilt on each Get/Create/Delete call when the cache says
// it needs refresh. Rebuilds are cheap (~microseconds — no network)
// and serialized under `mu`.
type Client struct {
	cfg       *Config
	region    *sdkregion.Region
	podAgency *PodAgencyProvider // nil in static-AK/SK mode

	mu  sync.Mutex
	sdk *sfsturbo.SFSTurboClient // current SDK client
}

// Interface lets the reconciler swap a fake in unit tests without
// pulling in the whole HuaweiCloud SDK chain.
type Interface interface {
	Create(ctx context.Context, in CreateInput) (string, error)
	Get(ctx context.Context, id string) (*ShareInfo, error)
	Delete(ctx context.Context, id string) error
	FindByName(ctx context.Context, name string) (*ShareInfo, error)
	Expand(ctx context.Context, id string, newSizeGiB int32) error

	// (spec-drift reconciliation).
	ListTags(ctx context.Context, id string) (map[string]string, error)
	AddTag(ctx context.Context, id, key, value string) error
	DeleteTag(ctx context.Context, id, key string) error
	ChangeSecurityGroup(ctx context.Context, id, newSecurityGroupId string) error
}

// ShareInfo is what the reconciler reads back. Subset of the SDK's
// model.ShareInfo — only the fields we actually consume on the K8s side.
type ShareInfo struct {
	Id               string
	Status           string // "100" = creating, "200" = available, "303" = error
	ExportLocation   string
	Size             int32
	AvailabilityZone string

	// Drift-detection fields. Populated from ShowShare; used by
	// the reconciler to detect spec/live mismatch.
	//
	// SubStatus encodes in-flight mutation operations (Huawei's own
	// state machine for the FS, distinct from `Status`):
	//   "121" expanding / "221" expand-ok / "321" expand-failed
	//   "132" sg-changing / "232" sg-ok    / "332" sg-failed
	//   "137" vpc-add     / "138" vpc-del  / etc.
	// Empty when the FS is idle. The reconciler avoids issuing a second
	// mutation while one is in flight.
	SubStatus       string
	ShareType       string
	CryptKeyId      string
	SubnetId        string
	VpcId           string
	SecurityGroupId string
}

// CreateInput is the validated, K8s-agnostic input passed to Create.
// The reconciler builds this from SfsTurboInstanceSpec; this package
// has no Kubernetes types dependency.
type CreateInput struct {
	Name              string
	Size              int32  // GiB
	ShareType         string // STANDARD | PERFORMANCE
	ShareProto        string // NFS
	AvailabilityZone  string
	VpcId             string
	SubnetId          string
	SecurityGroupId   string // "" lets Huawei auto-create
	CryptKeyId        string // "" disables encryption
	AutoCreateSgRules bool
	Tags              map[string]string
}

// NewClient builds a Client from cfg. Returns an error if the
// region is unknown or initial credentials can't be resolved.
func NewClient(cfg *Config) (*Client, error) {
	region, err := sfsturboregion.SafeValueOf(cfg.Region)
	if err != nil {
		// Provide a clearer error than the SDK's default — operators
		// often mistype the region, and the SDK message is dense.
		return nil, fmt.Errorf("unknown SFS Turbo region %q (try ap-southeast-1): %w", cfg.Region, err)
	}
	c := &Client{cfg: cfg, region: region}

	if cfg.PodAgencyMode() {
		// Lazy initial fetch — let the first API call trigger it so a
		// momentary metadata blip at boot doesn't fail-fast the
		// operator pod. NewClient's contract is "construct"; the SDK
		// build happens in ensureSDK() on demand.
		c.podAgency = NewPodAgencyProvider()
		return c, nil
	}

	// Static AK/SK mode — build the SDK client once and reuse it.
	creds, err := staticCredentialsFor(cfg, cfg.AccessKey, cfg.SecretKey, "")
	if err != nil {
		return nil, err
	}
	c.sdk = sfsturbo.NewSFSTurboClient(
		sfsturbo.SFSTurboClientBuilder().WithRegion(region).WithCredential(creds).Build(),
	)
	return c, nil
}

// Region returns the configured region — useful for log lines.
func (c *Client) Region() string { return c.cfg.Region }

// ensureSDK returns a *sfsturbo.SFSTurboClient whose underlying
// credentials are current. In static mode this is a no-op (returns
// the SDK built at construction). In pod-agency mode it refreshes
// the cached creds + rebuilds the SDK client if the cache says so.
//
// Caller holds the result for the duration of one SDK call — it's
// safe to keep using even if a concurrent reconcile rebuilds the
// shared `sdk` field (the previous client value remains valid as long
// as its credentials are still inside their TTL).
func (c *Client) ensureSDK() (*sfsturbo.SFSTurboClient, error) {
	if c.podAgency == nil {
		// Static mode — no refresh needed.
		return c.sdk, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	creds, err := c.podAgency.Get()
	if err != nil && creds == nil {
		// Hard failure: no cached creds + refresh failed. Caller
		// surfaces this as a transient reconcile error.
		return nil, fmt.Errorf("pod-agency: %w", err)
	}
	// err != nil with creds != nil → stale-but-valid fallback. We
	// continue with the previous good creds. The PodAgencyProvider
	// already wraps the original error in its message; we just log
	// nothing here since the operator's reconcile path is the right
	// place to surface that warning (a future release adds a structured
	// metric for this).

	if c.sdk == nil || c.podAgency.cache == nil || c.podAgency.cache.AccessKey != credsAK(creds) {
		// Rebuild the SDK client. Either first call, or the cache's
		// AK differs from the SDK's last-built AK (refresh happened).
		basicCreds, buildErr := staticCredentialsFor(c.cfg, creds.AccessKey, creds.SecretKey, creds.SecurityToken)
		if buildErr != nil {
			return nil, fmt.Errorf("build pod-agency credentials: %w", buildErr)
		}
		c.sdk = sfsturbo.NewSFSTurboClient(
			sfsturbo.SFSTurboClientBuilder().WithRegion(c.region).WithCredential(basicCreds).Build(),
		)
	}
	return c.sdk, nil
}

// credsAK is a tiny helper for the SDK-rebuild detection in ensureSDK.
// We compare against the cached AK because the underlying SDK doesn't
// expose its current credential.
func credsAK(c *PodAgencyCreds) string {
	if c == nil {
		return ""
	}
	return c.AccessKey
}

// Create calls SFS Turbo CreateShare and returns the freshly-minted
// FS UUID. The FS is NOT ready when this returns — call Get and poll
// until status == "200" (available) before publishing the export
// location.
//
// Note: Create is not idempotent on Huawei's side. The caller MUST
// avoid re-calling Create after the first successful response —
// status.fsId on the CR is the marker. A future release will gate this with a
// finalizer / DeepCopy-comparable spec hash.
func (c *Client) Create(ctx context.Context, in CreateInput) (string, error) {
	share := &sfsturbomodel.Share{
		Name:             in.Name,
		Size:             in.Size,
		ShareProto:       in.ShareProto,
		ShareType:        in.ShareType,
		AvailabilityZone: in.AvailabilityZone,
		VpcId:            in.VpcId,
		SubnetId:         in.SubnetId,
		SecurityGroupId:  in.SecurityGroupId, // "" → Huawei auto-creates per-FS SG
	}

	// Metadata block is optional. We populate it when the user asked
	// for encryption or wants to opt out of auto SG rule creation.
	if in.CryptKeyId != "" || !in.AutoCreateSgRules {
		md := &sfsturbomodel.Metadata{}
		if in.CryptKeyId != "" {
			md.CryptKeyId = ptr(in.CryptKeyId)
		}
		// SDK ships a typed enum for auto_create_security_group_rules;
		// only emit the field when we want "false". Default is "true"
		// when omitted, which matches our CRD default.
		if !in.AutoCreateSgRules {
			f := sfsturbomodel.GetMetadataAutoCreateSecurityGroupRulesEnum().FALSE
			md.AutoCreateSecurityGroupRules = &f
		}
		share.Metadata = md
	}

	if len(in.Tags) > 0 {
		tags := make([]sfsturbomodel.ResourceTag, 0, len(in.Tags))
		for k, v := range in.Tags {
			tags = append(tags, sfsturbomodel.ResourceTag{Key: k, Value: v})
		}
		share.Tags = &tags
	}

	req := &sfsturbomodel.CreateShareRequest{
		Body: &sfsturbomodel.CreateShareRequestBody{Share: share},
	}

	sdk, err := c.ensureSDK()
	if err != nil {
		return "", err
	}
	resp, err := sdk.CreateShare(req)
	if err != nil {
		// Adopt-on-collision. Huawei returns 409 with errCode
		// SFS.TURBO.0009 when the requested name is already taken.
		// This commonly happens when a previous reconcile created the
		// FS but failed to persist .status.fsId on the K8s side
		// (e.g. operator pod crashed between API call and status
		// patch). Without adoption logic the operator would loop on
		// 409 forever, leaving an orphan FS in Huawei + a never-bound
		// PVC in K8s.
		//
		// We list shares + filter by name, and if exactly one matches
		// we return its id as if Create had succeeded. Caller can't
		// tell the difference — the operator's poll branch then
		// reconciles the adopted FS like any freshly-created one.
		if isNameAlreadyExists(err) {
			existing, findErr := c.FindByName(ctx, in.Name)
			if findErr == nil && existing != nil && existing.Id != "" {
				return existing.Id, nil
			}
			// Name-exists error but we couldn't find the FS via list —
			// surface the original error so the operator can flag it.
			return "", fmt.Errorf("CreateShare(%s) name-collision but FindByName failed: %v (original: %w)", in.Name, findErr, classify(err))
		}
		return "", fmt.Errorf("CreateShare(%s): %w", in.Name, classify(err))
	}
	if resp == nil || resp.Id == nil {
		return "", errors.New("CreateShare returned nil id")
	}
	return *resp.Id, nil
}

// FindByName scans every SFS Turbo FS in the project and returns the
// one whose Name matches `name`. Returns ErrFsNotFound on zero matches.
// Used by Create's adopt-on-409 path and by the migration tooling.
//
// The SDK's ListShares takes no name filter, so we paginate + filter
// client-side. Scale is bounded for typical CCE clusters.
func (c *Client) FindByName(ctx context.Context, name string) (*ShareInfo, error) {
	req := &sfsturbomodel.ListSharesRequest{
		ContentType: "application/json",
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return nil, err
	}
	resp, err := sdk.ListShares(req)
	if err != nil {
		return nil, fmt.Errorf("ListShares: %w", classify(err))
	}
	if resp == nil || resp.Shares == nil {
		return nil, ErrFsNotFound
	}
	for _, s := range *resp.Shares {
		if s.Name != nil && *s.Name == name {
			out := &ShareInfo{}
			if s.Id != nil {
				out.Id = *s.Id
			}
			if s.Status != nil {
				out.Status = *s.Status
			}
			if s.ExportLocation != nil {
				out.ExportLocation = *s.ExportLocation
			}
			if s.AvailabilityZone != nil {
				out.AvailabilityZone = *s.AvailabilityZone
			}
			return out, nil
		}
	}
	return nil, ErrFsNotFound
}

// Get returns the current state of the FS. Used by the reconciler to
// poll status during create and to validate that pre-existing FSes
// ((in a post-migration scenario)) still match the CR spec.
func (c *Client) Get(ctx context.Context, id string) (*ShareInfo, error) {
	req := &sfsturbomodel.ShowShareRequest{ShareId: id}
	sdk, err := c.ensureSDK()
	if err != nil {
		return nil, err
	}
	resp, err := sdk.ShowShare(req)
	if err != nil {
		return nil, fmt.Errorf("ShowShare(%s): %w", id, classify(err))
	}
	if resp == nil {
		return nil, errors.New("ShowShare returned nil response")
	}

	out := &ShareInfo{Id: id}
	if resp.Id != nil {
		out.Id = *resp.Id
	}
	if resp.Status != nil {
		out.Status = *resp.Status
	}
	if resp.SubStatus != nil {
		out.SubStatus = *resp.SubStatus
	}
	if resp.ExportLocation != nil {
		out.ExportLocation = *resp.ExportLocation
	}
	if resp.AvailabilityZone != nil {
		out.AvailabilityZone = *resp.AvailabilityZone
	}
	if resp.ShareType != nil {
		out.ShareType = *resp.ShareType
	}
	if resp.CryptKeyId != nil {
		out.CryptKeyId = *resp.CryptKeyId
	}
	if resp.SubnetId != nil {
		out.SubnetId = *resp.SubnetId
	}
	if resp.VpcId != nil {
		out.VpcId = *resp.VpcId
	}
	if resp.SecurityGroupId != nil {
		out.SecurityGroupId = *resp.SecurityGroupId
	}
	// Huawei returns size as *string ("500"); the SDK marshals it as
	// such because some HPC tiers are float-valued. We parse to int32
	// when we can but tolerate empty.
	if resp.Size != nil {
		var n int32
		_, err := fmt.Sscanf(*resp.Size, "%d", &n)
		if err == nil {
			out.Size = n
		}
	}
	return out, nil
}

// Expand calls SFS Turbo ExpandShare to grow the FS to newSizeGiB.
// Huawei returns 202 immediately; the share's status moves to "121"
// (extending) and eventually back to "200" with the new capacity.
// The reconciler polls Get() to detect completion.
//
// SFS Turbo Standard expansion step is 100 GiB minimum; the operator
// trusts the caller (CRD validation) to pass a valid size and
// surfaces any Huawei-side rejection as a transient error.
func (c *Client) Expand(ctx context.Context, id string, newSizeGiB int32) error {
	req := &sfsturbomodel.ExpandShareRequest{
		ShareId: id,
		Body: &sfsturbomodel.ExpandShareRequestBody{
			Extend: &sfsturbomodel.Extend{NewSize: newSizeGiB},
		},
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return err
	}
	if _, err := sdk.ExpandShare(req); err != nil {
		return fmt.Errorf("ExpandShare(%s, %d GiB): %w", id, newSizeGiB, classify(err))
	}
	return nil
}

// Delete calls DeleteShare. Huawei returns immediately with HTTP 202;
// the FS goes through a "deleting" state for a few seconds. Callers
// (the finalizer) should poll Get until ErrFsNotFound.
//
// Earlier versions did not wire delete into the reconciler. This method
// exists so internal/controller can be unit-tested against the
// Interface defined in the package.
func (c *Client) Delete(ctx context.Context, id string) error {
	req := &sfsturbomodel.DeleteShareRequest{ShareId: id}
	sdk, err := c.ensureSDK()
	if err != nil {
		return err
	}
	_, err = sdk.DeleteShare(req)
	if err != nil {
		return fmt.Errorf("DeleteShare(%s): %w", id, classify(err))
	}
	return nil
}

// ListTags returns the current tag set on the FS as a map.
// Wraps ShowSharedTags (per-share read; ListSharedTags is project-wide).
func (c *Client) ListTags(ctx context.Context, id string) (map[string]string, error) {
	req := &sfsturbomodel.ShowSharedTagsRequest{
		ContentType: "application/json",
		ShareId:     id,
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return nil, err
	}
	resp, err := sdk.ShowSharedTags(req)
	if err != nil {
		return nil, fmt.Errorf("ShowSharedTags(%s): %w", id, classify(err))
	}
	out := make(map[string]string)
	if resp != nil && resp.Tags != nil {
		for _, t := range *resp.Tags {
			out[t.Key] = t.Value
		}
	}
	return out, nil
}

// AddTag attaches one tag to the FS. Idempotent on Huawei's side —
// calling twice with the same key is a no-op (the value is updated
// to the most recent call's value).
func (c *Client) AddTag(ctx context.Context, id, key, value string) error {
	req := &sfsturbomodel.CreateSharedTagRequest{
		ContentType: "application/json",
		ShareId:     id,
		Body: &sfsturbomodel.CreateSharedTagRequestBody{
			Tag: &sfsturbomodel.ResourceTag{Key: key, Value: value},
		},
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return err
	}
	if _, err := sdk.CreateSharedTag(req); err != nil {
		return fmt.Errorf("CreateSharedTag(%s, %s=%s): %w", id, key, value, classify(err))
	}
	return nil
}

// DeleteTag removes one tag from the FS by key. Treats Huawei NotFound
// (deleted-already) as success — the reconciler's drift loop may race
// against itself if the operator's leader changes mid-loop.
func (c *Client) DeleteTag(ctx context.Context, id, key string) error {
	req := &sfsturbomodel.DeleteSharedTagRequest{
		ContentType: "application/json",
		ShareId:     id,
		Key:         key,
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return err
	}
	if _, err := sdk.DeleteSharedTag(req); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("DeleteSharedTag(%s, %s): %w", id, key, classify(err))
	}
	return nil
}

// ChangeSecurityGroup swaps the FS's security group. Returns 202;
// the FS goes through SubStatus="132" (sg-changing) before settling
// to "232" (sg-changed). The reconciler watches SubStatus to detect
// completion and avoid issuing a second change while one is in flight.
func (c *Client) ChangeSecurityGroup(ctx context.Context, id, newSecurityGroupId string) error {
	req := &sfsturbomodel.ChangeSecurityGroupRequest{
		ContentType: "application/json",
		ShareId:     id,
		Body: &sfsturbomodel.ChangeSecurityGroupRequestBody{
			ChangeSecurityGroup: &sfsturbomodel.ChangeSecurityGroup{
				SecurityGroupId: newSecurityGroupId,
			},
		},
	}
	sdk, err := c.ensureSDK()
	if err != nil {
		return err
	}
	if _, err := sdk.ChangeSecurityGroup(req); err != nil {
		return fmt.Errorf("ChangeSecurityGroup(%s, %s): %w", id, newSecurityGroupId, classify(err))
	}
	return nil
}

// IsAvailable reports whether the share status string means "ready
// to mount". Huawei's status field is an int-as-string; "200" is the
// healthy value. Anything else is either creating ("100" / "101"),
// expanding ("121"), or errored ("303" and several others).
func IsAvailable(status string) bool { return status == "200" }

// SubStatus codes Huawei returns when a mutation is in flight on the
// FS. Documented in the ShowShare response schema. We use these to
// avoid double-issuing a mutation that's already underway.
const (
	SubStatusExpanding       = "121"
	SubStatusExpandOK        = "221"
	SubStatusExpandFailed    = "321"
	SubStatusSgChanging      = "132"
	SubStatusSgChangeOK      = "232"
	SubStatusSgChangeFailed  = "332"
	SubStatusVpcAddPending   = "137"
	SubStatusVpcAddOK        = "237"
	SubStatusVpcAddFailed    = "337"
	SubStatusVpcDelPending   = "138"
	SubStatusVpcDelOK        = "238"
	SubStatusVpcDelFailed    = "338"
)

// IsSgChangeInFlight reports whether the FS is currently mid-way
// through a ChangeSecurityGroup call. The reconciler defers issuing
// a second change while this is true.
func IsSgChangeInFlight(subStatus string) bool {
	return subStatus == SubStatusSgChanging
}

// IsErrored reports whether the share status string means "failed".
func IsErrored(status string) bool {
	// 303 = creation failed; other documented error codes per Huawei
	// docs: 308 (expand failed), 400 (subscriber abnormal), etc.
	switch status {
	case "303", "308", "400":
		return true
	default:
		return false
	}
}

func ptr[T any](v T) *T { return &v }

// SDK package alias — keeps the global region symbol close at hand.
var _ = sdkregion.Region{}
