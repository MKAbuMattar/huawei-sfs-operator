/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

package huawei

// CCE pod-bound IAM agency credential provider — production path.
//
// CCE supports binding an IAM agency to a Kubernetes ServiceAccount via
// the `cce.huawei.com/agency` annotation. Pods using that SA can read
// short-lived credentials from the OpenStack-style instance metadata
// service:
//
//	GET http://169.254.169.254/openstack/latest/securitykey
//	→   {
//	      "credential": {
//	        "access":        "<AK>",
//	        "secret":        "<SK>",
//	        "securitytoken": "<token>",
//	        "expires_at":    "2026-05-10T18:42:00.000Z"
//	      }
//	    }
//
// The official Huawei Go SDK ships a metadata accessor (see
// core/auth/internal/metadata_accessor.go) but it does IMDSv2-style
// PUT-then-GET. CCE returns 400 on the PUT, and the SDK's fallback
// only catches 404/405/503, so the SDK errors out. This file
// implements the single-GET path that's known to work against CCE.
//
// Caching:
//   - Refresh once we're past 50% of TTL (REFRESH_RATIO) so transient
//     metadata blips don't strand us with a dead token.
//   - Hard cap at 10 min regardless (MAX_CACHE_AGE) — bounds the
//     blast radius if a wedged metadata response keeps the same
//     token forever.
//   - Stale-but-valid fallback: if a refresh fails AND we have a
//     valid cached cred, keep using it until it self-expires.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	metadataURL       = "http://169.254.169.254/openstack/latest/securitykey"
	metadataTimeout   = 5 * time.Second
	refreshRatio      = 0.5
	maxCacheAge       = 10 * time.Minute
	minRefreshLatency = 60 * time.Second // never refresh more often than every 60s
)

// metadataPayload is the shape Huawei's metadata service returns.
type metadataPayload struct {
	Credential struct {
		Access        string `json:"access"`
		Secret        string `json:"secret"`
		SecurityToken string `json:"securitytoken"`
		ExpiresAt     string `json:"expires_at"`
	} `json:"credential"`
}

// PodAgencyCreds holds one snapshot of credentials returned by the
// metadata service. Immutable once constructed.
type PodAgencyCreds struct {
	AccessKey     string
	SecretKey     string
	SecurityToken string
	FetchedAt     time.Time
	ExpiresAt     time.Time
}

// needsRefresh reports whether the cached creds are too close to expiry
// (or too old) to keep using.
func (c *PodAgencyCreds) needsRefresh(now time.Time) bool {
	age := now.Sub(c.FetchedAt)
	if age > maxCacheAge {
		return true
	}
	ttl := c.ExpiresAt.Sub(c.FetchedAt)
	threshold := time.Duration(float64(ttl) * refreshRatio)
	if threshold < minRefreshLatency {
		threshold = minRefreshLatency
	}
	return age >= threshold
}

// IsExpired reports whether the security token is past its expiry.
// Used to surface "creds dead" errors when a refresh isn't possible.
func (c *PodAgencyCreds) IsExpired(now time.Time) bool {
	return !now.Before(c.ExpiresAt)
}

// PodAgencyProvider fetches + caches pod-bound IAM agency credentials
// from CCE's metadata service. Thread-safe; one instance per operator.
type PodAgencyProvider struct {
	httpClient *http.Client
	url        string

	mu    sync.Mutex
	cache *PodAgencyCreds
}

// NewPodAgencyProvider builds a provider with sane defaults. Doesn't
// fetch — the first Get() call triggers the initial fetch so the
// operator can boot even if the metadata service is briefly down.
func NewPodAgencyProvider() *PodAgencyProvider {
	return &PodAgencyProvider{
		httpClient: &http.Client{Timeout: metadataTimeout},
		url:        metadataURL,
	}
}

// Get returns the most recent valid credentials. Refreshes if cache is
// missing or close to expiry. On refresh failure with a cached non-
// expired set, returns the cache and a wrapped warning (caller can log).
func (p *PodAgencyProvider) Get() (*PodAgencyCreds, error) {
	now := time.Now().UTC()
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cache != nil && !p.cache.needsRefresh(now) {
		return p.cache, nil
	}

	fresh, err := p.fetch()
	if err != nil {
		// Stale-but-valid fallback. Honest about it via the returned
		// error so the caller can log a warning.
		if p.cache != nil && !p.cache.IsExpired(now) {
			return p.cache, fmt.Errorf("metadata refresh failed; using cached creds expiring at %s: %w", p.cache.ExpiresAt.Format(time.RFC3339), err)
		}
		return nil, err
	}
	p.cache = fresh
	return fresh, nil
}

// fetch performs a single IMDSv1-style GET and parses the response.
// Caller holds p.mu.
func (p *PodAgencyProvider) fetch() (*PodAgencyCreds, error) {
	req, err := http.NewRequest(http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build metadata request: %w", err)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata service unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read metadata response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata service returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var payload metadataPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse metadata JSON: %w", err)
	}
	cred := payload.Credential
	if cred.Access == "" || cred.Secret == "" {
		return nil, errors.New("metadata response missing access/secret")
	}
	expiresAt, err := time.Parse(time.RFC3339, cred.ExpiresAt)
	if err != nil {
		// Huawei sometimes returns "2026-05-10T18:42:00.000Z" — RFC3339
		// requires "2026-05-10T18:42:00Z" but tolerates fractional sec.
		// Try a more lenient parser.
		expiresAt, err = time.Parse("2006-01-02T15:04:05.000Z", cred.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse expires_at %q: %w", cred.ExpiresAt, err)
		}
	}
	return &PodAgencyCreds{
		AccessKey:     cred.Access,
		SecretKey:     cred.Secret,
		SecurityToken: cred.SecurityToken,
		FetchedAt:     time.Now().UTC(),
		ExpiresAt:     expiresAt,
	}, nil
}

// Reset clears the cache. Used by tests; not normally called in prod.
func (p *PodAgencyProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = nil
}
