package controller

import (
	"errors"
	"fmt"
)

// ErrorCategory classifies controller errors for retry policy decisions.
type ErrorCategory int

const (
	ErrKube       ErrorCategory = iota // Kubernetes API errors (retriable)
	ErrCloudflare                      // Cloudflare API errors (retriable)
	ErrConfig                          // Configuration/validation errors (permanent)
	ErrFinalizer                       // Finalizer lifecycle errors (retriable)
)

// ControllerError is a categorized error for reconciliation.
type ControllerError struct {
	Category ErrorCategory
	Err      error
}

func (e *ControllerError) Error() string {
	var prefix string
	switch e.Category {
	case ErrKube:
		prefix = "Kubernetes API error"
	case ErrCloudflare:
		prefix = "Cloudflare API error"
	case ErrConfig:
		prefix = "configuration error"
	case ErrFinalizer:
		prefix = "finalizer error"
	}
	return fmt.Sprintf("%s: %v", prefix, e.Err)
}

func (e *ControllerError) Unwrap() error {
	return e.Err
}

func KubeError(err error) *ControllerError {
	return &ControllerError{Category: ErrKube, Err: err}
}

func CloudflareError(err error) *ControllerError {
	return &ControllerError{Category: ErrCloudflare, Err: err}
}

func ConfigError(msg string) *ControllerError {
	return &ControllerError{Category: ErrConfig, Err: fmt.Errorf("%s", msg)}
}

func FinalizerError(err error) *ControllerError {
	return &ControllerError{Category: ErrFinalizer, Err: err}
}

// IsPermanent returns true if the error should not be retried.
// Only configuration errors are permanent.
func IsPermanent(err error) bool {
	var ce *ControllerError
	if errors.As(err, &ce) {
		return ce.Category == ErrConfig
	}
	return false
}
