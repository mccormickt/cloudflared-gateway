package controller

import (
	"errors"
	"fmt"
)

// errorCategory classifies controller errors for retry policy decisions.
type errorCategory int

const (
	errKube       errorCategory = iota // Kubernetes API errors (retriable)
	errCloudflare                      // Cloudflare API errors (retriable)
	errConfig                          // Configuration/validation errors (permanent)
	errFinalizer                       // Finalizer lifecycle errors (retriable)
)

// ControllerError is a categorized error for reconciliation.
type ControllerError struct {
	category errorCategory
	err      error
}

func (e *ControllerError) Error() string {
	var prefix string
	switch e.category {
	case errKube:
		prefix = "Kubernetes API error"
	case errCloudflare:
		prefix = "Cloudflare API error"
	case errConfig:
		prefix = "configuration error"
	case errFinalizer:
		prefix = "finalizer error"
	default:
		prefix = "unknown error"
	}
	return fmt.Sprintf("%s: %v", prefix, e.err)
}

func (e *ControllerError) Unwrap() error {
	return e.err
}

func KubeError(err error) *ControllerError {
	return &ControllerError{category: errKube, err: err}
}

func CloudflareError(err error) *ControllerError {
	return &ControllerError{category: errCloudflare, err: err}
}

func ConfigError(msg string) *ControllerError {
	return &ControllerError{category: errConfig, err: errors.New(msg)}
}

func FinalizerError(err error) *ControllerError {
	return &ControllerError{category: errFinalizer, err: err}
}

// IsPermanent returns true if the error should not be retried.
// Only configuration errors are permanent.
func IsPermanent(err error) bool {
	var ce *ControllerError
	if errors.As(err, &ce) {
		return ce.category == errConfig
	}
	return false
}
