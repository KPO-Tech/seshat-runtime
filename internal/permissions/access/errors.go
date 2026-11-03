package access

import (
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// PermissionDeniedError is returned when the sandbox denies an action outright.
type PermissionDeniedError struct {
	Reason string
}

func (e *PermissionDeniedError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		return "permission denied"
	}
	return "permission denied: " + reason
}

// ApprovalRequiredError is returned when an action is valid but needs approval.
type ApprovalRequiredError struct {
	Reason string
}

func (e *ApprovalRequiredError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		return "approval required"
	}
	return "approval required: " + reason
}

// ErrorForDecision converts a normalized decision into a conventional Go error.
func ErrorForDecision(result DecisionResult) error {
	switch result.Decision {
	case DecisionAllow:
		return nil
	case DecisionAsk:
		return &ApprovalRequiredError{Reason: result.Reason}
	default:
		return &PermissionDeniedError{Reason: result.Reason}
	}
}

// ErrorForPermissionResult converts a runtime permission result into a conventional Go error.
func ErrorForPermissionResult(result types.PermissionResult, fallbackReason string) error {
	if result.IsAllowed() || result.IsPassthrough() {
		return nil
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = strings.TrimSpace(fallbackReason)
	}
	if result.IsAsk() {
		return &ApprovalRequiredError{Reason: reason}
	}
	return &PermissionDeniedError{Reason: reason}
}
