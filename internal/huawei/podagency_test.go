/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

package huawei

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPodAgencyCreds_NeedsRefresh(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name      string
		fetchedAt time.Time
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "fresh — far from 50% TTL",
			fetchedAt: now.Add(-30 * time.Second), // 30s old, 6h TTL → 0.14% — fresh
			expiresAt: now.Add(6 * time.Hour),
			want:      false,
		},
		{
			name:      "halfway — should refresh",
			fetchedAt: now.Add(-3 * time.Hour), // 3h old, 6h TTL → 50%
			expiresAt: now.Add(3 * time.Hour),
			want:      true,
		},
		{
			name:      "over MAX_CACHE_AGE — refresh even if not 50%",
			fetchedAt: now.Add(-11 * time.Minute), // 11m old, but TTL is 6h
			expiresAt: now.Add(6 * time.Hour),
			want:      true,
		},
		{
			name:      "very recent — under minRefreshLatency (60s) regardless",
			fetchedAt: now.Add(-10 * time.Second),
			expiresAt: now.Add(10 * time.Second), // TTL=20s, 50%=10s; minRefreshLatency=60s wins
			want:      false,                     // still fresh because 10s < 60s
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &PodAgencyCreds{FetchedAt: tc.fetchedAt, ExpiresAt: tc.expiresAt}
			if got := c.needsRefresh(now); got != tc.want {
				t.Errorf("needsRefresh: got=%v want=%v (age=%v ttl=%v)", got, tc.want, now.Sub(tc.fetchedAt), tc.expiresAt.Sub(tc.fetchedAt))
			}
		})
	}
}

func TestPodAgencyProvider_FetchAndCache(t *testing.T) {
	var calls atomic.Int64
	mockMetadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"credential": {
				"access":        "AKfake",
				"secret":        "SKfake",
				"securitytoken": "TOKfake",
				"expires_at":    "2099-12-31T23:59:59.000Z"
			}
		}`))
	}))
	defer mockMetadata.Close()

	p := &PodAgencyProvider{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		url:        mockMetadata.URL,
	}

	for i := 0; i < 5; i++ {
		creds, err := p.Get()
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		if creds.AccessKey != "AKfake" || creds.SecretKey != "SKfake" || creds.SecurityToken != "TOKfake" {
			t.Errorf("creds mismatch: %+v", creds)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 fetch (cache hit on 4 subsequent calls), got %d", got)
	}
}

func TestPodAgencyProvider_StaleButValidFallback(t *testing.T) {
	var calls atomic.Int64
	mockMetadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// First call: success.
			_, _ = w.Write([]byte(`{
				"credential": {
					"access": "AK1", "secret": "SK1", "securitytoken": "T1",
					"expires_at": "2099-12-31T23:59:59.000Z"
				}
			}`))
			return
		}
		// Subsequent calls: error.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("metadata unavailable"))
	}))
	defer mockMetadata.Close()

	p := &PodAgencyProvider{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		url:        mockMetadata.URL,
	}

	// First call seeds cache.
	first, err := p.Get()
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.AccessKey != "AK1" {
		t.Fatalf("first AK wrong: %q", first.AccessKey)
	}

	// Force cache to look stale so next Get triggers a fetch.
	p.mu.Lock()
	p.cache.FetchedAt = time.Now().UTC().Add(-30 * time.Minute) // past maxCacheAge
	p.mu.Unlock()

	// Second call: refresh fails, but cache is still inside its expiry
	// window → return cache + non-nil err.
	second, err := p.Get()
	if err == nil {
		t.Fatalf("expected wrapped error on stale-but-valid path")
	}
	if second == nil || second.AccessKey != "AK1" {
		t.Errorf("stale-but-valid fallback should return cached creds, got=%+v", second)
	}
}
