/*
Copyright 2026 The huawei-sfs-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package huawei wraps the HuaweiCloud Go SDK v3 SFS Turbo client. The
// reconciler talks only to the small surface area exposed here.
package huawei

import (
	"fmt"
	"os"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
)

// Config carries the operator-level Huawei API config. ProjectId +
// Region map onto Huawei's IAM scoping. Source is env-vars set on the
// operator Pod (see deploy/helm/sfs-operator/templates/).
type Config struct {
	// ProjectId — Huawei IAM project ID for Region. Required.
	// Env var: HUAWEI_PROJECT_ID.
	ProjectId string

	// Region — e.g. "ap-southeast-1". Required. Env var: HUAWEI_REGION.
	Region string

	// AccessKey / SecretKey — optional, static AK/SK fallback. When
	// both are set the operator uses them directly (no rotation).
	// Intended for local `make run` development; in cluster prefer
	// pod-agency mode where these env vars are unset and the operator
	// pulls short-lived rotating creds from CCE's metadata service.
	//
	// Env vars: HUAWEI_ACCESS_KEY, HUAWEI_SECRET_KEY.
	AccessKey string
	SecretKey string
}

// PodAgencyMode returns true when the operator should use the
// pod-bound IAM agency credential provider (single-GET to
// 169.254.169.254). Mode is "static AK/SK" when both env vars are set,
// otherwise pod-agency.
func (c *Config) PodAgencyMode() bool {
	return c.AccessKey == "" || c.SecretKey == ""
}

// LoadConfigFromEnv builds a Config from the operator's environment.
// Returns an error if required fields are missing — fail-fast at
// startup is preferable to per-reconcile errors later.
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ProjectId: os.Getenv("HUAWEI_PROJECT_ID"),
		Region:    os.Getenv("HUAWEI_REGION"),
		AccessKey: os.Getenv("HUAWEI_ACCESS_KEY"),
		SecretKey: os.Getenv("HUAWEI_SECRET_KEY"),
	}
	if cfg.ProjectId == "" {
		return nil, fmt.Errorf("HUAWEI_PROJECT_ID env var is required")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("HUAWEI_REGION env var is required")
	}
	return cfg, nil
}

// staticCredentialsFor builds a long-lived basic.Credentials from
// cfg.AccessKey + cfg.SecretKey. Used in static-AK/SK mode (and as
// the per-refresh constructor for pod-agency mode).
func staticCredentialsFor(cfg *Config, accessKey, secretKey, securityToken string) (auth.ICredential, error) {
	builder := basic.NewCredentialsBuilder().
		WithAk(accessKey).
		WithSk(secretKey).
		WithProjectId(cfg.ProjectId)
	if securityToken != "" {
		builder = builder.WithSecurityToken(securityToken)
	}
	creds, err := builder.SafeBuild()
	if err != nil {
		return nil, fmt.Errorf("build credentials: %w", err)
	}
	return creds, nil
}
