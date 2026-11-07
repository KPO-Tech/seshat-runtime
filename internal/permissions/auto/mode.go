package auto

import (
	"context"
	"fmt"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/internal/utils"
)

// Classifier is the interface for permission classification in auto mode.
// The classifier predicts whether a tool use should be allowed or denied.
type Classifier interface {
	// Classify predicts if a tool use should be allowed.
	// Returns the classification result with allowed flag, confidence, and reason.
	Classify(ctx context.Context, toolName string, input map[string]any) (Classification, error)
}

// AdvancedClassifierInterface extends Classifier with transcript support.
// This enables the two-stage classifier with context awareness.
type AdvancedClassifierInterface interface {
	// ClassifyWithTranscript predicts if a tool use should be allowed, using conversation context.
	// The messages parameter provides transcript context for better classification.
	// Returns ClassifierResult with detailed stage information.
	ClassifyWithTranscript(ctx context.Context, toolName string, toolInput map[string]any, messages []types.Message, toolUseID string) (*ClassifierResult, error)
}

// Classification represents the classifier's prediction.
type Classification struct {
	Allowed    bool    // Whether the tool use is predicted to be safe
	Confidence float64 // Confidence level (0-1) for the prediction
	Reason     string  // Human-readable explanation for the classification
}

// ModeConfig holds configuration for auto mode behavior.
type ModeConfig struct {
	// FailClosed governs a classifier that is configured but errors at
	// classification time: true denies, false asks. It does not apply when
	// no classifier was ever configured at all - that case always asks (or
	// denies in a headless context, which cannot show the prompt); see
	// Mode.Classify.
	FailClosed bool
}

// DefaultModeConfig is the default configuration for auto mode.
// FailClosed is true by default for security.
var DefaultModeConfig = &ModeConfig{
	FailClosed: true,
}

// PowerShell tool name constant.
var POWERSHELL_TOOL_NAME = "powershell"

// RequestPermissionsToolName is the name of the tool the model calls to
// explicitly escalate beyond its current authorization (see
// internal/tools/special/request_permissions). It must never be routed
// back through the classifier below: the classifier's own block
// categories (privilege escalation, filesystem access) would otherwise
// deny the very request whose entire purpose is asking a human, trapping
// the session with no way to ever reach a real approval prompt.
const RequestPermissionsToolName = "request_permissions"

// SafeToolNames is the allowlist of tool names that are safe and don't need
// classifier checking. These are read-only tools or tools that only affect
// internal metadata and don't modify the filesystem or system state.
// Aligned with OpenClaude's safe tool allowlist.
var SafeToolNames = []string{
	"read_file", "grep", "glob",
	"ask_user_question", "web_search", "web_fetch",
}

// isSafeTool checks if a tool name is in the safe-tool allowlist.
// These tools skip the expensive classifier call entirely.
func isSafeTool(toolName string) bool {
	for _, name := range SafeToolNames {
		if name == toolName {
			return true
		}
	}
	return false
}

// ClassifierContext provides context for auto mode classification.
// This mirrors the PermissionContext structure used by the permissions engine.
type ClassifierContext struct {
	ToolName                     string
	ToolInput                    map[string]any
	Mode                         types.PermissionMode
	SessionID                    types.SessionID
	TurnID                       types.TurnID
	ToolUseID                    string
	Messages                     []types.Message
	ShouldAvoidPermissionPrompts bool // Whether to avoid permission prompts (headless mode)
	Additional                   map[string]any
	DenialTracking               *types.DenialTrackingState
}

// Mode handles auto mode permission classification.
// It integrates with the permissions engine to provide classifier-based
// permission decisions for auto mode.
type Mode struct {
	classifier         Classifier
	advancedClassifier AdvancedClassifierInterface
	config             *ModeConfig
	advancedConfig     AdvancedConfig
}

// AdvancedConfig holds configuration for the two-stage (advanced) classifier.
type AdvancedConfig struct {
	// TwoStageClassifier is the model name for the second-stage thinking classifier.
	// If empty, two-stage classification is disabled.
	TwoStageClassifier string

	// MaxTranscriptChars is the maximum characters to include in the transcript.
	MaxTranscriptChars int

	// UseCacheControl enables cache control headers for classifier calls.
	UseCacheControl bool

	// EnableThinking enables the thinking/reasoning token output.
	EnableThinking bool
}

// DefaultAdvancedConfig returns the default configuration for advanced classification.
func DefaultAdvancedConfig() AdvancedConfig {
	return AdvancedConfig{
		TwoStageClassifier: "",
		MaxTranscriptChars: 200000,
		UseCacheControl:    true,
		EnableThinking:     true,
	}
}

// NewMode creates a new auto mode handler with the given classifier and config.
// If config is nil, DefaultModeConfig is used.
func NewMode(classifier Classifier, config *ModeConfig) *Mode {
	if config == nil {
		config = DefaultModeConfig
	}
	return &Mode{
		classifier:     classifier,
		config:         config,
		advancedConfig: DefaultAdvancedConfig(),
	}
}

// NewModeWithAdvancedClassifier creates a new auto mode handler with both
// basic and advanced classifier support.
func NewModeWithAdvancedClassifier(
	classifier Classifier,
	advancedClassifier AdvancedClassifierInterface,
	config *ModeConfig,
	advancedConfig AdvancedConfig,
) *Mode {
	if config == nil {
		config = DefaultModeConfig
	}
	if advancedConfig.TwoStageClassifier == "" {
		advancedConfig = DefaultAdvancedConfig()
	}
	return &Mode{
		classifier:         classifier,
		advancedClassifier: advancedClassifier,
		config:             config,
		advancedConfig:     advancedConfig,
	}
}

// SetClassifier updates the classifier used for auto mode decisions.
func (m *Mode) SetClassifier(classifier Classifier) {
	m.classifier = classifier
}

// SetAdvancedClassifier sets the advanced two-stage classifier.
func (m *Mode) SetAdvancedClassifier(classifier AdvancedClassifierInterface) {
	m.advancedClassifier = classifier
}

// Classify determines the permission decision for a tool use in auto mode.
// It implements several fast-paths aligned with OpenClaude:
//   - PowerShell special handling: always requires explicit permission
//   - Safe-tool allowlist: read-only tools skip the classifier
//   - No classifier configured: asks (denies if headless)
//   - Classifier configured but erroring: handled per FailClosed
//   - Denial tracking: tracks consecutive/total denials for fallback
//
// Returns a PermissionResult indicating allow/deny/ask behavior.
func (m *Mode) Classify(ctx context.Context, pctx *ClassifierContext) (types.PermissionResult, error) {
	// Initialize denial tracking if not provided
	if pctx.DenialTracking == nil {
		pctx.DenialTracking = &types.DenialTrackingState{}
	}

	// Fast path -1: request_permissions always reaches a human, never the
	// classifier. This tool exists specifically so the model can escalate
	// when it hits a block it believes is wrong for the current task; if the
	// classifier gets to re-judge it, its own "privilege operations"/"file
	// access" categories deny it identically to the original action, and the
	// escalation path silently dead-ends instead of ever asking the user.
	if pctx.ToolName == RequestPermissionsToolName {
		if pctx.ShouldAvoidPermissionPrompts {
			return types.DenyWithDecisionReason(
				"request_permissions requires interactive approval",
				&types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonAsyncAgent,
					Source: "request_permissions",
					Reason: "request_permissions requires interactive approval and permission prompts are not available in this context",
				},
			), nil
		}
		return types.AskWithDecisionReason("explicit permission escalation requested", &types.PermissionDecisionReason{
			Type:   types.PermissionDecisionReasonMode,
			Source: "request_permissions",
			Reason: "explicit permission escalation requested",
		}), nil
	}

	// Fast path 0: PowerShell special handling
	// PowerShell is a high-risk tool that always requires explicit user permission
	// in auto mode, even with a classifier. This is aligned with OpenClaude.
	if pctx.ToolName == POWERSHELL_TOOL_NAME {
		if pctx.ShouldAvoidPermissionPrompts {
			// In headless mode, deny because we can't prompt the user
			return types.DenyWithDecisionReason(
				"PowerShell tool requires interactive approval",
				&types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonAsyncAgent,
					Source: "powershell",
					Reason: "PowerShell tool requires interactive approval and permission prompts are not available in this context",
				},
			), nil
		}
		// In interactive mode, always ask for PowerShell
		return types.AskWithDecisionReason(
			"PowerShell tool requires explicit user permission",
			&types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "powershell",
				Reason: "PowerShell tool requires explicit user permission",
			},
		), nil
	}

	// Fast path 1: safe-tool allowlist skips the classifier entirely
	// Read-only tools and metadata-only tools are always safe
	if isSafeTool(pctx.ToolName) {
		pctx.DenialTracking.RecordSuccess()
		return types.PermissionResult{
			Behavior:     types.PermissionBehaviorAllow,
			Reason:       "tool is on the safe allowlist",
			UpdatedInput: utils.CloneInput(pctx.ToolInput),
			DecisionReason: &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "auto",
				Reason: "tool is on the safe allowlist",
			},
		}, nil
	}

	// No classifier was ever configured (as opposed to one that is
	// configured but erroring below, which FailClosed still governs): ask a
	// human rather than silently allow (see TODO.md M2) or silently deny
	// (which would make auto mode a dead end for every tool not already on
	// the safe-tool/safe-workflow allowlists, with no way to proceed). A
	// headless/background agent cannot show that prompt, so it denies
	// instead - same rule the PowerShell and request_permissions fast paths
	// above already apply, and unlike those, this result returns directly
	// from checkGlobalPermission's Step 4 without passing through
	// applyModeTransformations' headless-ask-to-deny conversion, so it must
	// decide this itself rather than relying on that.
	if m.classifier == nil && m.advancedClassifier == nil {
		if pctx.ShouldAvoidPermissionPrompts {
			return types.DenyWithDecisionReason(
				fmt.Sprintf("permission to use %s denied: no classifier configured and permission prompts are not available in this context", pctx.ToolName),
				&types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonAsyncAgent,
					Source: "classifier",
					Reason: "auto mode but no classifier configured, and this is a headless context",
				},
			), nil
		}
		return types.AskWithDecisionReason("auto mode but no classifier configured", &types.PermissionDecisionReason{
			Type:   types.PermissionDecisionReasonMode,
			Source: "classifier",
			Reason: "auto mode but no classifier configured",
		}), nil
	}

	var (
		allowed    bool
		confidence float64
		reason     string
	)
	if m.advancedClassifier != nil {
		result, err := m.advancedClassifier.ClassifyWithTranscript(ctx, pctx.ToolName, pctx.ToolInput, pctx.Messages, pctx.ToolUseID)
		if err != nil {
			if m.config.FailClosed {
				return types.DenyWithDecisionReason(
					fmt.Sprintf("permission to use %s denied: classification failed", pctx.ToolName),
					&types.PermissionDecisionReason{
						Type:   types.PermissionDecisionReasonMode,
						Source: "classifier",
						Reason: fmt.Sprintf("classification failed: %v", err),
					},
				), nil
			}
			return types.AskWithDecisionReason(fmt.Sprintf("classification failed: %v", err), &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonOther,
				Source: "classifier",
				Reason: err.Error(),
			}), nil
		}
		if result.Unavailable {
			// The check could not decide (its model did not answer in a form that can be read, the call failed twice). That is no verdict
			// on the action: with a person there, they decide; without one, nothing is done.
			reason := fmt.Sprintf("the safety check could not decide on %s: %s", pctx.ToolName, result.Reason)
			if pctx.ShouldAvoidPermissionPrompts {
				return types.DenyWithDecisionReason(reason, &types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonAsyncAgent,
					Source: "classifier",
					Reason: reason,
				}), nil
			}
			return types.AskWithDecisionReason(reason, &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonOther,
				Source: "classifier",
				Reason: reason,
			}), nil
		}
		allowed = !result.ShouldBlock
		confidence = 1.0
		reason = result.Reason
	} else {
		// Call the classifier to make a permission decision
		classification, err := m.classifier.Classify(ctx, pctx.ToolName, pctx.ToolInput)
		if err != nil {
			if m.config.FailClosed {
				// Fail-closed: deny on classifier errors
				return types.DenyWithDecisionReason(
					fmt.Sprintf("permission to use %s denied: classification failed", pctx.ToolName),
					&types.PermissionDecisionReason{
						Type:   types.PermissionDecisionReasonMode,
						Source: "classifier",
						Reason: fmt.Sprintf("classification failed: %v", err),
					},
				), nil
			}
			// Fail-open: fall back to ask on classifier errors
			return types.AskWithDecisionReason(fmt.Sprintf("classification failed: %v", err), &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonOther,
				Source: "classifier",
				Reason: err.Error(),
			}), nil
		}
		allowed = classification.Allowed
		confidence = classification.Confidence
		reason = classification.Reason
	}

	// Classifier allowed the tool use
	if allowed {
		pctx.DenialTracking.RecordSuccess()
		return types.PermissionResult{
			Behavior:     types.PermissionBehaviorAllow,
			Reason:       reason,
			UpdatedInput: utils.CloneInput(pctx.ToolInput),
			Confidence:   &confidence,
			DecisionReason: &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonTool,
				Source: "classifier",
				Reason: reason,
			},
		}, nil
	}

	// Classifier denied the tool use
	// Record denial and check if we should fallback to prompting
	pctx.DenialTracking.RecordDenial()
	defaultConfig := DefaultDenialLimitConfig()

	// Check if we should fallback to prompting due to denial limits
	// This prevents infinite loops of classifier denials in auto mode
	if fallbackResult := HandleDenialLimitExceeded(
		pctx.DenialTracking,
		&defaultConfig,
		pctx.ShouldAvoidPermissionPrompts,
		reason,
	); fallbackResult != nil {
		return *fallbackResult, nil
	}

	// No fallback - return the classifier's denial
	result := types.DenyWithDecisionReason(reason, &types.PermissionDecisionReason{
		Type:   types.PermissionDecisionReasonTool,
		Source: "classifier",
		Reason: reason,
	})
	result.DenialTracking = pctx.DenialTracking
	return result, nil
}
