/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

package huawei

import (
	"errors"
	"fmt"
	"strings"

	sdkerr "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/sdkerr"
)

// ErrFsNotFound is returned by Get when the share id no longer exists
// Huawei-side. The reconciler treats this as "FS was deleted out-of-
// band" — either someone hit the console or a `terraform destroy`
// raced us. Status writeback clears `.status.fsId` so the next
// reconcile retries Create.
var ErrFsNotFound = errors.New("sfs turbo share not found")

// classify maps a Huawei SDK error to one of our sentinel errors when
// possible. Returns the original error otherwise (callers should
// inspect via errors.Is(ErrFsNotFound) / errors.Is(ErrTransient)).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var serviceErr *sdkerr.ServiceResponseError
	if errors.As(err, &serviceErr) {
		// Huawei returns 404 for "not found". Some endpoints also use
		// 200 with an error body — the SDK still wraps those into a
		// ServiceResponseError with StatusCode populated.
		if serviceErr.StatusCode == 404 {
			return fmt.Errorf("%w: %s", ErrFsNotFound, serviceErr.ErrorMessage)
		}
	}
	return err
}

// isNotFound reports whether the SDK error is a 404 / NotFound.
// Used by DeleteTag idempotency branch.
func isNotFound(err error) bool {
	var serviceErr *sdkerr.ServiceResponseError
	if !errors.As(err, &serviceErr) {
		return false
	}
	return serviceErr.StatusCode == 404
}

// isNameAlreadyExists reports whether the SDK error is "FS with this
// name already exists" (HTTP 409, Huawei error code SFS.TURBO.0009).
// Used by Create's adopt-on-collision branch.
//
// Huawei nests the JSON error inside ErrorMessage, e.g.:
//
//	{"errCode":"SFS.TURBO.0009","errMsg":"name have existed"}
//
// We probe both the status code and the message text so a future
// SDK version that lifts the errCode out doesn't regress.
func isNameAlreadyExists(err error) bool {
	var serviceErr *sdkerr.ServiceResponseError
	if !errors.As(err, &serviceErr) {
		return false
	}
	if serviceErr.StatusCode != 409 {
		return false
	}
	msg := serviceErr.ErrorMessage
	return strings.Contains(msg, "SFS.TURBO.0009") || strings.Contains(strings.ToLower(msg), "name have existed") || strings.Contains(strings.ToLower(msg), "name already exists")
}
