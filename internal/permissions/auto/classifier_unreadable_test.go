package auto

import (
	"context"
	"errors"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// scriptedAPI answers the classifier with the next of its replies (an empty reply stands for a failed call), and keeps the last one when it
// has no more.
type scriptedAPI struct {
	replies []string
	calls   int
}

func (s *scriptedAPI) Classify(context.Context, *ClassifierAPIRequest) (*ClassifierAPIResponse, error) {
	i := s.calls
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	s.calls++
	if s.replies[i] == "" {
		return nil, errors.New("HTTP 400")
	}
	return &ClassifierAPIResponse{Text: s.replies[i]}, nil
}

func writeFileContext(headless bool) *ClassifierContext {
	return &ClassifierContext{
		ToolName:                     "write_file",
		ToolInput:                    map[string]any{"path": "page.html"},
		Mode:                         types.PermissionModeAuto,
		ShouldAvoidPermissionPrompts: headless,
	}
}

func modeWith(api ClassifierAPI) *Mode {
	m := NewMode(nil, &ModeConfig{FailClosed: true})
	m.SetAdvancedClassifier(NewTwoStageClassifierWithAPI(DefaultTwoStageConfig(), api))
	return m
}

// The failure seen with glm-5.3: the call of the check failed, nothing could be read, and the write was refused without a word to the person.
func TestACheckThatCannotDecideAsksThePersonInsteadOfRefusing(t *testing.T) {
	api := &scriptedAPI{replies: []string{""}}
	result, err := modeWith(api).Classify(context.Background(), writeFileContext(false))
	if err != nil {
		t.Fatal(err)
	}
	if result.Behavior != types.PermissionBehaviorAsk {
		t.Fatalf("a check that could not decide must ask the person, got %v (%s)", result.Behavior, result.Reason)
	}
	if api.calls < 3 {
		t.Fatalf("each stage asks once more before giving up (fast stage twice, thinking stage twice), got %d calls", api.calls)
	}
}

func TestACheckThatCannotDecideDeniesWhereNobodyCanAnswer(t *testing.T) {
	result, err := modeWith(&scriptedAPI{replies: []string{""}}).Classify(context.Background(), writeFileContext(true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Behavior != types.PermissionBehaviorDeny {
		t.Fatalf("a background agent cannot be asked: got %v", result.Behavior)
	}
}

func TestAFailedCallIsAskedAgainOnce(t *testing.T) {
	// The first call of the fast stage fails, the second says the action is fine: no question, no refusal.
	api := &scriptedAPI{replies: []string{"", "<block>no</block>"}}
	result, err := modeWith(api).Classify(context.Background(), writeFileContext(false))
	if err != nil {
		t.Fatal(err)
	}
	if result.Behavior != types.PermissionBehaviorAllow {
		t.Fatalf("a second call that answers must be used, got %v (%s)", result.Behavior, result.Reason)
	}
}

func TestAnActionTheCheckBlocksStaysBlocked(t *testing.T) {
	result, err := modeWith(&scriptedAPI{replies: []string{"<block>yes</block>"}}).Classify(context.Background(), writeFileContext(false))
	if err != nil {
		t.Fatal(err)
	}
	if result.Behavior != types.PermissionBehaviorDeny {
		t.Fatalf("a verdict to block is a refusal, not a question: got %v", result.Behavior)
	}
}
