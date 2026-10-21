package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

type fakeTitleGenerator struct {
	title string
	calls chan string
}

func (f fakeTitleGenerator) GenerateTitle(ctx context.Context, firstUserMsg string) (string, error) {
	if f.calls != nil {
		f.calls <- firstUserMsg
	}
	return f.title, nil
}

func TestNewClientPropagatesPromptFnToAskUserTool(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		PromptFn: func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
			if request.Type != types.PromptTypeChoice {
				t.Fatalf("expected choice prompt, got %q", request.Type)
			}
			if request.Message != "Proceed?" {
				t.Fatalf("unexpected prompt message %q", request.Message)
			}
			return types.PromptResponse{Value: "Yes"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	registeredTool, ok := client.registry.Get("ask_user_question")
	if !ok {
		t.Fatal("expected ask_user_question tool to be registered")
	}

	result, callErr := registeredTool.Call(context.Background(), tool.CallInput{
		Parsed: map[string]any{
			"questions": []any{
				map[string]any{
					"question": "Proceed?",
					"header":   "Confirm",
					"options": []any{
						map[string]any{"label": "Yes"},
						map[string]any{"label": "No"},
					},
				},
			},
		},
	}, nil)
	if callErr != nil {
		t.Fatalf("tool call failed: %v", callErr)
	}
	if result.IsError() {
		t.Fatalf("expected AskUserQuestion success, got %#v", result)
	}
	if !strings.Contains(result.Content, "A: Yes") {
		t.Fatalf("expected prompt-driven answer in tool output, got %q", result.Content)
	}
}

// fakePlanStore records the arguments its one call received - used to prove
// ClientConfig.UserID actually reaches PlanStore.CreateOrUpdate, instead of
// silently staying "" (see TestNewClientPropagatesUserIDToSubmitPlanTool's
// doc comment for what broke without it).
type fakePlanStore struct {
	gotUserID    string
	gotSessionID string
}

func (f *fakePlanStore) CreateOrUpdate(_ context.Context, planID, sessionID, userID, _, _, _ string) (string, int, error) {
	f.gotUserID = userID
	f.gotSessionID = sessionID
	if planID == "" {
		planID = "plan-test-id"
	}
	return planID, 1, nil
}

func (f *fakePlanStore) SetStatus(context.Context, string, string) error { return nil }

// TestNewClientPropagatesUserIDToSubmitPlanTool is a regression test: a host
// with a real PlanStore (e.g. seshat-backend) persists every plan document
// submit_plan creates - without ClientConfig.UserID being threaded through
// to PlanStore.CreateOrUpdate, every one of those was stored with an empty
// UserID, which then failed that host's own ownership check ("doc.UserID !=
// principal.User.ID") on every later read or patch, for every user,
// unconditionally.
func TestNewClientPropagatesUserIDToSubmitPlanTool(t *testing.T) {
	store := &fakePlanStore{}
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		PlanStore:       store,
		UserID:          "user-42",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	registeredTool, ok := client.registry.Get("submit_plan")
	if !ok {
		t.Fatal("expected submit_plan tool to be registered")
	}

	toolCtx := tool.NewToolUseContext(types.SessionID("session-abc"), "", "", types.PermissionModeOnRequest)
	toolCtx.ExecutionMode = "plan"
	result, callErr := registeredTool.Call(context.Background(), tool.CallInput{
		SessionID:   types.SessionID("session-abc"),
		ToolContext: &toolCtx,
		Parsed: map[string]any{
			"slug":    "test-plan",
			"content": "1. Do the thing",
		},
	}, nil)
	if callErr != nil {
		t.Fatalf("tool call failed: %v", callErr)
	}
	if result.IsError() {
		t.Fatalf("expected submit_plan success, got %#v", result)
	}
	if store.gotUserID != "user-42" {
		t.Fatalf("expected PlanStore.CreateOrUpdate to receive UserID %q, got %q", "user-42", store.gotUserID)
	}
	if store.gotSessionID != "session-abc" {
		t.Fatalf("expected PlanStore.CreateOrUpdate to receive SessionID %q, got %q", "session-abc", store.gotSessionID)
	}
}

func TestCreateSessionUsesConfiguredWorkingDirAsRootPath(t *testing.T) {
	workingDir := t.TempDir()

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		WorkingDir:      workingDir,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer session.Close()

	if got := session.GetMetadata().RootPath; got != workingDir {
		t.Fatalf("expected session root path %q, got %q", workingDir, got)
	}
}

type sdkMonoRunTestTool struct{}

type sdkClosableBackend struct {
	SessionBackend
	closeCalls int
}

func newSDKClosableBackend() *sdkClosableBackend {
	return &sdkClosableBackend{SessionBackend: NewMemorySessionBackend()}
}

func (b *sdkClosableBackend) Close() error {
	b.closeCalls++
	return nil
}

func (sdkMonoRunTestTool) Definition() tool.Definition {
	return tool.Definition{Name: "sdk_stub", Description: "sdk stub"}
}

func (sdkMonoRunTestTool) Call(ctx context.Context, input tool.CallInput, permissionCheck types.CanUseToolFn) (tool.CallResult, error) {
	if got := input.Parsed["value"]; got != "x" {
		return tool.CallResult{}, fmt.Errorf("unexpected input: %#v", input.Parsed)
	}
	return tool.NewTextResult("sdk tool ok"), nil
}

func (sdkMonoRunTestTool) Description(ctx context.Context) (string, error) { return "sdk stub", nil }
func (sdkMonoRunTestTool) ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error) {
	return input, nil
}
func (sdkMonoRunTestTool) CheckPermissions(ctx context.Context, input map[string]any, toolCtx tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(input)
}
func (sdkMonoRunTestTool) IsConcurrencySafe(input map[string]any) bool { return false }
func (sdkMonoRunTestTool) IsReadOnly(input map[string]any) bool        { return false }
func (sdkMonoRunTestTool) IsEnabled() bool                             { return true }
func (sdkMonoRunTestTool) FormatResult(data any) string                { return fmt.Sprintf("%v", data) }
func (sdkMonoRunTestTool) BackfillInput(ctx context.Context, input map[string]any) map[string]any {
	return input
}

func TestAskCompletesStreamingMultiToolMonoRun(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected sdk provider payload to expose sdk_stub, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "Checking both.",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "sdk_stub",
						"input": map[string]any{},
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"value":"x"}`,
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_2",
						"name":  "sdk_stub",
						"input": map[string]any{},
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"value":"x"}`,
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 2 {
				t.Fatalf("expected 2 tool_result blocks in second sdk provider request, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "done",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		default:
			payloadBytes, _ := json.Marshal(payload)
			t.Fatalf("unexpected sdk provider request %d: %s", requests, string(payloadBytes))
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions:        false,
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check both", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 provider requests, got %d", requests)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if len(response.ToolUses) != 2 {
		t.Fatalf("expected 2 tool uses, got %d", len(response.ToolUses))
	}
	if len(response.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(response.ToolResults))
	}
	if !response.IsComplete {
		t.Fatal("expected sdk ask response to be complete")
	}
}

func TestAskEmitsStructuredRuntimeEvents(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected sdk provider payload to expose sdk_stub, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "Checking runtime events.",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "sdk_stub",
						"input": map[string]any{},
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"value":"x"}`,
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool_result block in second sdk provider request, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "done",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		default:
			payloadBytes, _ := json.Marshal(payload)
			t.Fatalf("unexpected sdk provider request %d: %s", requests, string(payloadBytes))
		}
	}))
	defer server.Close()

	var emitted []RuntimeEvent
	client, err := NewClient(&ClientConfig{
		PersistSessions:        false,
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		RuntimeEventFn: func(event RuntimeEvent) {
			emitted = append(emitted, event)
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check events", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if requests != 2 {
		t.Fatalf("expected 2 provider requests, got %d", requests)
	}

	var (
		sawTurnStarted   bool
		sawTurnCompleted bool
		sawChunk         bool
		sawToolProgress  bool
	)
	for _, event := range emitted {
		switch event.Type {
		case RuntimeEventTypeTurnStarted:
			sawTurnStarted = true
		case RuntimeEventTypeTurnCompleted:
			sawTurnCompleted = true
			if event.StopReason != types.StopReasonEndTurn {
				t.Fatalf("expected end_turn stop reason, got %q", event.StopReason)
			}
			if event.TurnNumber != 1 {
				t.Fatalf("expected completed turn number 1, got %d", event.TurnNumber)
			}
		case RuntimeEventTypeResponseChunk:
			if event.Chunk != nil && event.Chunk.Delta == "done" {
				sawChunk = true
			}
		case RuntimeEventTypeToolProgress:
			if event.ToolProgress != nil && event.ToolProgress.ToolName == "sdk_stub" {
				sawToolProgress = true
			}
		}
	}
	if !sawTurnStarted {
		t.Fatal("expected turn.started runtime event")
	}
	if !sawTurnCompleted {
		t.Fatal("expected turn.completed runtime event")
	}
	if !sawChunk {
		t.Fatal("expected response.chunk runtime event for final text delta")
	}
	if !sawToolProgress {
		t.Fatal("expected tool.progress runtime event for sdk_stub")
	}
}

func TestAskCompletesOpenAIMonoRun(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if payload["stream"] == false {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-title",
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role":    "assistant",
							"content": "Check Both",
						},
					},
				},
			})
			return
		}
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected openai payload to expose sdk_stub, payload=%#v", payload)
			}
			if stream, _ := payload["stream"].(bool); !stream {
				t.Fatalf("expected native openai streaming request, got %#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"reasoning_content": "Checking both.",
							},
						},
					},
				},
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"tool_calls": []map[string]any{
									{
										"index": 0,
										"id":    "call_1",
										"type":  "function",
										"function": map[string]any{
											"name":      "sdk_stub",
											"arguments": `{"value":"x"}`,
										},
									},
									{
										"index": 1,
										"id":    "call_2",
										"type":  "function",
										"function": map[string]any{
											"name":      "sdk_stub",
											"arguments": `{"value":"x"}`,
										},
									},
								},
							},
							"finish_reason": "tool_calls",
						},
					},
					"usage": map[string]any{
						"prompt_tokens":     14,
						"completion_tokens": 8,
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 2 {
				t.Fatalf("expected 2 tool_result messages in second openai payload, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-2",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"content": "done",
							},
							"finish_reason": "stop",
						},
					},
					"usage": map[string]any{
						"prompt_tokens":     18,
						"completion_tokens": 3,
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			payloadBytes, _ := json.Marshal(payload)
			t.Fatalf("unexpected openai provider request %d: %s", requests, string(payloadBytes))
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions:        false,
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderOpenAI,
			Model:    "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderOpenAI,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check both", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 openai provider requests, got %d", requests)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if len(response.ToolUses) != 2 {
		t.Fatalf("expected 2 tool uses, got %d", len(response.ToolUses))
	}
	if len(response.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(response.ToolResults))
	}
}

func TestAskStreamsResponseChunksToHost(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if payload["stream"] == false {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-title",
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role":    "assistant",
							"content": "Check Both",
						},
					},
				},
			})
			return
		}
		switch requests {
		case 1:
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"content": "Checking both.",
							},
						},
					},
				},
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"tool_calls": []map[string]any{
									{
										"index": 0,
										"id":    "call_1",
										"type":  "function",
										"function": map[string]any{
											"name":      "sdk_stub",
											"arguments": `{"value":"x"}`,
										},
									},
								},
							},
							"finish_reason": "tool_calls",
						},
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-2",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"content": "done",
							},
							"finish_reason": "stop",
						},
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			payloadBytes, _ := json.Marshal(payload)
			t.Fatalf("unexpected openai provider request %d: %s", requests, string(payloadBytes))
		}
	}))
	defer server.Close()

	var chunks []ResponseChunk
	client, err := NewClient(&ClientConfig{
		PersistSessions:        false,
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderOpenAI,
			Model:    "gpt-4o-mini",
		},
		ResponseChunkFn: func(chunk ResponseChunk) {
			chunks = append(chunks, chunk)
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderOpenAI,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check both", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}

	var sawTextDelta bool
	var sawToolStart bool
	var sawMessageStop bool
	for _, chunk := range chunks {
		if chunk.Type == ResponseChunkTypeContentBlockDelta && chunk.DeltaType == "text_delta" && chunk.Delta == "done" {
			sawTextDelta = true
		}
		if chunk.Type == ResponseChunkTypeContentBlockStart {
			if toolUse, ok := any(chunk.ContentBlock).(ToolUseContent); ok && toolUse.Name == "sdk_stub" {
				sawToolStart = true
			}
		}
		if chunk.Type == ResponseChunkTypeMessageStop {
			sawMessageStop = true
		}
	}

	if !sawTextDelta {
		t.Fatal("expected streamed text delta callback")
	}
	if !sawToolStart {
		t.Fatal("expected streamed tool start callback")
	}
	if !sawMessageStop {
		t.Fatal("expected streamed message stop callback")
	}
}

func TestNewClientUsesInjectedSessionBackend(t *testing.T) {
	backend := NewMemorySessionBackend()
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionBackend:  backend,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	if client.store == nil {
		t.Fatal("expected client store to be initialized from injected session backend")
	}

	sessionID := types.SessionID("session-from-custom-backend")
	if err := client.store.SaveSession(sessionID, &types.SessionMetadata{
		ID:         sessionID,
		Status:     types.SessionStatusActive,
		CreatedAt:  time.Unix(1700000100, 0).UTC(),
		UpdatedAt:  time.Unix(1700000101, 0).UTC(),
		TotalTurns: 1,
	}); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session from custom backend, got %d", len(sessions))
	}
	if sessions[0].ID != sessionID {
		t.Fatalf("expected listed session %q, got %q", sessionID, sessions[0].ID)
	}
}

func TestClientSearchTranscriptsByContentUsesConfiguredStore(t *testing.T) {
	backend := NewMemorySessionBackend()
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionBackend:  backend,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	matchingID := types.SessionID("session-matching-transcript")
	otherID := types.SessionID("session-other-transcript")
	for _, sessionID := range []types.SessionID{matchingID, otherID} {
		if err := client.store.SaveSession(sessionID, &types.SessionMetadata{
			ID:        sessionID,
			Status:    types.SessionStatusActive,
			CreatedAt: time.Unix(1700000300, 0).UTC(),
			UpdatedAt: time.Unix(1700000301, 0).UTC(),
		}); err != nil {
			t.Fatalf("SaveSession(%s) failed: %v", sessionID, err)
		}
	}

	if err := client.store.ReplaceTranscript(matchingID, []types.TranscriptEntry{{
		ID:        types.MessageID("msg-matching"),
		Type:      types.EntryTypeMessage,
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.TextContent{Text: "Needle phrase in transcript"}},
		Timestamp: time.Unix(1700000302, 0).UTC(),
	}}); err != nil {
		t.Fatalf("ReplaceTranscript(matching) failed: %v", err)
	}
	if err := client.store.ReplaceTranscript(otherID, []types.TranscriptEntry{{
		ID:        types.MessageID("msg-other"),
		Type:      types.EntryTypeMessage,
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.TextContent{Text: "No useful match here"}},
		Timestamp: time.Unix(1700000303, 0).UTC(),
	}}); err != nil {
		t.Fatalf("ReplaceTranscript(other) failed: %v", err)
	}

	sessionIDs, err := client.SearchTranscriptsByContent("needle phrase", 10)
	if err != nil {
		t.Fatalf("SearchTranscriptsByContent failed: %v", err)
	}
	if len(sessionIDs) != 1 || sessionIDs[0] != matchingID {
		t.Fatalf("expected only %s, got %#v", matchingID, sessionIDs)
	}
}

func TestNewClientUsesSQLiteSessionBackend(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		PersistSessions:   false,
		SessionSQLitePath: filepath.Join(t.TempDir(), "seshat.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	if client.store == nil {
		t.Fatal("expected client store to be initialized from sqlite session backend")
	}

	sessionID := types.SessionID("session-from-sqlite-backend")
	if err := client.store.SaveSession(sessionID, &types.SessionMetadata{
		ID:         sessionID,
		Status:     types.SessionStatusActive,
		CreatedAt:  time.Unix(1700000200, 0).UTC(),
		UpdatedAt:  time.Unix(1700000201, 0).UTC(),
		TotalTurns: 1,
	}); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session from sqlite backend, got %d", len(sessions))
	}
	if sessions[0].ID != sessionID {
		t.Fatalf("expected listed session %q, got %q", sessionID, sessions[0].ID)
	}
}

func TestClientCloseClosesOwnedSQLiteSessionBackend(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		PersistSessions:   false,
		SessionSQLitePath: filepath.Join(t.TempDir(), "seshat.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	sessionID := types.SessionID("closed-sqlite-backend")
	err = client.store.SaveSession(sessionID, &types.SessionMetadata{
		ID:        sessionID,
		Status:    types.SessionStatusActive,
		CreatedAt: time.Unix(1700000200, 0).UTC(),
		UpdatedAt: time.Unix(1700000201, 0).UTC(),
	})
	if err == nil {
		t.Fatal("expected store save to fail after owned sqlite backend is closed")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected closed database error, got %v", err)
	}
}

func TestClientCloseDoesNotCloseInjectedSessionBackend(t *testing.T) {
	backend := newSDKClosableBackend()
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionBackend:  backend,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if backend.closeCalls != 0 {
		t.Fatalf("expected injected backend to remain open, got %d close calls", backend.closeCalls)
	}
}

func TestClientCloseDoesNotCloseInjectedSessionStore(t *testing.T) {
	backend := newSDKClosableBackend()
	store, err := NewSessionStoreWithBackend(backend)
	if err != nil {
		t.Fatalf("NewStoreWithBackend failed: %v", err)
	}

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionStore:    store,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if backend.closeCalls != 0 {
		t.Fatalf("expected injected store backend to remain open, got %d close calls", backend.closeCalls)
	}
}

func TestClientGetSessionStoreReturnsConfiguredStore(t *testing.T) {
	store, err := NewSessionStoreWithBackend(NewMemorySessionBackend())
	if err != nil {
		t.Fatalf("NewSessionStoreWithBackend failed: %v", err)
	}

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionStore:    store,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if got := client.GetSessionStore(); got != store {
		t.Fatal("expected GetSessionStore to return the configured store")
	}
}

func TestClientSandboxKindReportsLocalByDefault(t *testing.T) {
	store, err := NewSessionStoreWithBackend(NewMemorySessionBackend())
	if err != nil {
		t.Fatalf("NewSessionStoreWithBackend failed: %v", err)
	}
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionStore:    store,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if got := client.SandboxKind(); got != SandboxKindLocal {
		t.Fatalf("expected SandboxKind() to report SandboxKindLocal when Docker was never requested, got %v", got)
	}
}

func TestClientSandboxKindFallsBackWhenDockerUnavailable(t *testing.T) {
	store, err := NewSessionStoreWithBackend(NewMemorySessionBackend())
	if err != nil {
		t.Fatalf("NewSessionStoreWithBackend failed: %v", err)
	}
	dockerCfg := sandbox.DefaultDockerConfig()
	dockerCfg.DockerBinary = "seshat-definitely-not-a-real-binary"
	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		SessionStore:    store,
		SandboxKind:     SandboxKindDocker,
		SandboxDocker:   dockerCfg,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if got := client.SandboxKind(); got != SandboxKindLocal {
		t.Fatalf("expected SandboxKind() to fall back to SandboxKindLocal when the configured Docker backend is unhealthy, got %v", got)
	}
}

func TestNewArtifactStoreFromConfigUsesSDKStorageConfig(t *testing.T) {
	store, err := NewArtifactStoreFromConfig(StorageConfig{
		Provider:  StorageProviderLocal,
		LocalPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewArtifactStoreFromConfig failed: %v", err)
	}

	ref, err := store.PutArtifact(context.Background(), ArtifactPutRequest{
		Namespace:   NamespaceDocuments,
		Filename:    "notes.txt",
		ContentType: "text/plain",
		Timestamp:   time.Unix(1700000300, 0).UTC(),
	}, []byte("hello sdk storage"))
	if err != nil {
		t.Fatalf("PutArtifact failed: %v", err)
	}

	if ref.Key == "" {
		t.Fatal("expected stored artifact key")
	}

	body, err := store.Get(context.Background(), ref.Key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got := string(body); got != "hello sdk storage" {
		t.Fatalf("unexpected stored body %q", got)
	}
}

func TestClientGetArtifactStoreReturnsConfiguredStore(t *testing.T) {
	store, err := NewArtifactStoreFromConfig(StorageConfig{
		Provider:  StorageProviderLocal,
		LocalPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewArtifactStoreFromConfig failed: %v", err)
	}

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		ArtifactStore:   store,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if got := client.GetArtifactStore(); got != store {
		t.Fatal("expected GetArtifactStore to return the configured store")
	}
}

func TestAskCompletesOllamaMonoRun(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected ollama payload to expose sdk_stub, payload=%#v", payload)
			}
			if stream, _ := payload["stream"].(bool); !stream {
				t.Fatalf("expected native ollama streaming request, got %#v", payload)
			}
			sdkWriteJSONL(t, w, []map[string]any{
				{"message": map[string]any{"content": "Checking both."}},
				{"message": map[string]any{"tool_calls": []map[string]any{
					{"function": map[string]any{"name": "sdk_stub", "arguments": map[string]any{"value": "x"}}},
					{"function": map[string]any{"name": "sdk_stub", "arguments": map[string]any{"value": "x"}}},
				}}},
			})
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 2 {
				t.Fatalf("expected 2 tool results in second ollama payload, got %d", got)
			}
			sdkWriteJSONL(t, w, []map[string]any{
				{"message": map[string]any{"content": "done"}},
			})
		default:
			payloadBytes, _ := json.Marshal(payload)
			t.Fatalf("unexpected ollama provider request %d: %s", requests, string(payloadBytes))
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions:        false,
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderOllama,
			Model:    "qwen2.5",
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderOllama,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check both", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 ollama provider requests, got %d", requests)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if len(response.ToolUses) != 2 || len(response.ToolResults) != 2 {
		t.Fatalf("expected 2 tool uses/results, got %d/%d", len(response.ToolUses), len(response.ToolResults))
	}
}

func TestAskCompletesGeminiMonoRun(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		if strings.Contains(r.URL.String(), ":generateContent") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"parts": []map[string]any{{"text": "Check Both"}},
						},
					},
				},
			})
			return
		}

		requests++
		if !strings.Contains(r.URL.String(), ":streamGenerateContent?alt=sse") {
			t.Fatalf("expected gemini stream endpoint, got %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected gemini payload to expose sdk_stub, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"parts": []map[string]any{{"text": "Checking both."}},
							},
						},
					},
				},
				{
					"candidates": []map[string]any{
						{
							"finishReason": "STOP",
							"content": map[string]any{
								"parts": []map[string]any{
									{"functionCall": map[string]any{"name": "sdk_stub", "args": map[string]any{"value": "x"}}},
									{"functionCall": map[string]any{"name": "sdk_stub", "args": map[string]any{"value": "x"}}},
								},
							},
						},
					},
				},
			})
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 2 {
				t.Fatalf("expected 2 tool results in second gemini payload, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"candidates": []map[string]any{
						{
							"finishReason": "STOP",
							"content": map[string]any{
								"parts": []map[string]any{{"text": "done"}},
							},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected gemini provider request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		PermissionMode:  types.PermissionModeBypass,
		AutoCompact:     false,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderGemini,
			Model:    "gemini-2.0-flash",
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderGemini,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "check both", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 gemini provider requests, got %d", requests)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if len(response.ToolUses) != 2 || len(response.ToolResults) != 2 {
		t.Fatalf("expected 2 tool uses/results, got %d/%d", len(response.ToolUses), len(response.ToolResults))
	}
}

func TestPersistedSessionsResumeOpenAIMultiTurn(t *testing.T) {
	testPersistedSessionResumeNativeProvider(t, persistedNativeProviderCase{
		provider: types.APIProviderOpenAI,
		model:    "gpt-4o-mini",
		writeToolTurn: func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{"content": "Working."},
						},
					},
				},
				{
					"id": "chatcmpl-1",
					"choices": []map[string]any{
						{
							"delta": map[string]any{
								"tool_calls": []map[string]any{
									{
										"index": 0,
										"id":    "call_1",
										"type":  "function",
										"function": map[string]any{
											"name":      "sdk_stub",
											"arguments": `{"value":"x"}`,
										},
									},
								},
							},
							"finish_reason": "tool_calls",
						},
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		},
		writeToolResultAck: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool result in second openai payload, got %d", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-2",
					"choices": []map[string]any{
						{
							"delta":         map[string]any{"content": "first done"},
							"finish_reason": "stop",
						},
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		},
		writeResumedTurn: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected persisted tool result in resumed openai payload, got %d", got)
			}
			if !sdkPayloadContainsText(payload, "first") || !sdkPayloadContainsText(payload, "first done") || !sdkPayloadContainsText(payload, "second") {
				t.Fatalf("expected resumed openai payload to contain prior transcript, payload=%#v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"id": "chatcmpl-3",
					"choices": []map[string]any{
						{
							"delta":         map[string]any{"content": "second done"},
							"finish_reason": "stop",
						},
					},
				},
			})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		},
	})
}

func TestPersistedSessionsResumeAnthropicMultiTurn(t *testing.T) {
	testPersistedSessionResumeNativeProvider(t, persistedNativeProviderCase{
		provider: types.APIProviderAnthropic,
		model:    "claude-3-5-sonnet-20241022",
		writeToolTurn: func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{"type": "content_block_start", "content_block": map[string]any{"type": "text", "text": ""}},
				{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "Working."}},
				{"type": "content_block_stop"},
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "tool_use", "id": "toolu_1", "name": "sdk_stub", "input": map[string]any{},
					},
				},
				{
					"type":  "content_block_delta",
					"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"value":"x"}`},
				},
				{"type": "content_block_stop"},
				{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
				{"type": "message_stop"},
			})
		},
		writeToolResultAck: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool_result in second anthropic request, got %d", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{"type": "content_block_start", "content_block": map[string]any{"type": "text", "text": ""}},
				{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "first done"}},
				{"type": "content_block_stop"},
				{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
				{"type": "message_stop"},
			})
		},
		writeResumedTurn: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected persisted tool result in resumed anthropic request, got %d", got)
			}
			if !sdkPayloadContainsText(payload, "first") || !sdkPayloadContainsText(payload, "first done") || !sdkPayloadContainsText(payload, "second") {
				t.Fatalf("expected resumed anthropic payload to contain prior transcript, payload=%#v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{"type": "content_block_start", "content_block": map[string]any{"type": "text", "text": ""}},
				{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "second done"}},
				{"type": "content_block_stop"},
				{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
				{"type": "message_stop"},
			})
		},
	})
}

func TestPersistedSessionsResumeOllamaMultiTurn(t *testing.T) {
	testPersistedSessionResumeNativeProvider(t, persistedNativeProviderCase{
		provider: types.APIProviderOllama,
		model:    "qwen2.5",
		writeToolTurn: func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			sdkWriteJSONL(t, w, []map[string]any{
				{"message": map[string]any{"content": "Working."}},
				{"message": map[string]any{"tool_calls": []map[string]any{
					{"function": map[string]any{"name": "sdk_stub", "arguments": map[string]any{"value": "x"}}},
				}}},
			})
		},
		writeToolResultAck: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool result in second ollama payload, got %d", got)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			sdkWriteJSONL(t, w, []map[string]any{
				{"message": map[string]any{"content": "first done"}},
			})
		},
		writeResumedTurn: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected persisted tool result in resumed ollama payload, got %d", got)
			}
			if !sdkPayloadContainsText(payload, "first") || !sdkPayloadContainsText(payload, "first done") || !sdkPayloadContainsText(payload, "second") {
				t.Fatalf("expected resumed ollama payload to contain prior transcript, payload=%#v", payload)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			sdkWriteJSONL(t, w, []map[string]any{
				{"message": map[string]any{"content": "second done"}},
			})
		},
	})
}

func TestPersistedSessionsResumeGeminiMultiTurn(t *testing.T) {
	testPersistedSessionResumeNativeProvider(t, persistedNativeProviderCase{
		provider: types.APIProviderGemini,
		model:    "gemini-2.0-flash",
		writeToolTurn: func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"candidates": []map[string]any{
						{
							"content": map[string]any{
								"parts": []map[string]any{{"text": "Working."}},
							},
						},
					},
				},
				{
					"candidates": []map[string]any{
						{
							"finishReason": "STOP",
							"content": map[string]any{
								"parts": []map[string]any{
									{"functionCall": map[string]any{"name": "sdk_stub", "args": map[string]any{"value": "x"}}},
								},
							},
						},
					},
				},
			})
		},
		writeToolResultAck: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool result in second gemini payload, got %d", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"candidates": []map[string]any{
						{
							"finishReason": "STOP",
							"content": map[string]any{
								"parts": []map[string]any{{"text": "first done"}},
							},
						},
					},
				},
			})
		},
		writeResumedTurn: func(t *testing.T, w http.ResponseWriter, payload map[string]any) {
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected persisted tool result in resumed gemini payload, got %d", got)
			}
			if !sdkPayloadContainsText(payload, "first") || !sdkPayloadContainsText(payload, "first done") || !sdkPayloadContainsText(payload, "second") {
				t.Fatalf("expected resumed gemini payload to contain prior transcript, payload=%#v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"candidates": []map[string]any{
						{
							"finishReason": "STOP",
							"content": map[string]any{
								"parts": []map[string]any{{"text": "second done"}},
							},
						},
					},
				},
			})
		},
	})
}

func TestPersistedSessionsCarryPlanModeAcrossRestore(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "enter_plan_mode") {
				t.Fatalf("expected EnterPlanMode in initial tool surface, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_plan_enter",
						"name":  "enter_plan_mode",
						"input": map[string]any{},
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 2:
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool_result block after EnterPlanMode, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "entered plan mode",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 3:
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected restored transcript to include prior plan tool result, got %d", got)
			}
			if !sdkProviderPayloadHasTool(payload, "exit_plan_mode") {
				t.Fatalf("expected ExitPlanMode in restored tool surface, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_plan_exit",
						"name":  "exit_plan_mode",
						"input": map[string]any{},
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"plan":"Ship it"}`,
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 4:
			if got := sdkCountToolResultsInPayload(payload); got != 2 {
				t.Fatalf("expected both plan tool results before final exit response, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "exited plan mode",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		default:
			t.Fatalf("unexpected provider request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions:        true,
		SessionStorageDir:      filepath.Join(t.TempDir(), "sessions"),
		PermissionMode:         types.PermissionModeOnRequest,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		PromptFn: func(ctx context.Context, request types.PromptRequest) (types.PromptResponse, error) {
			return types.PromptResponse{Value: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	firstResponse, err := session.SubmitMessage(context.Background(), "plan this")
	if err != nil {
		t.Fatalf("first SubmitMessage failed: %v", err)
	}
	if last := firstResponse.Messages[len(firstResponse.Messages)-1]; extractTextContent(last) != "entered plan mode" {
		t.Fatalf("expected first final text %q, got %q", "entered plan mode", extractTextContent(last))
	}
	if got := session.GetPermissionMode(); got != types.PermissionModeOnRequest {
		t.Fatalf("expected session to keep approval mode %q after EnterPlanMode, got %q", types.PermissionModeOnRequest, got)
	}
	if got := session.GetExecutionMode(); got != ExecutionModePlan {
		t.Fatalf("expected session to enter execution mode %q after EnterPlanMode, got %q", ExecutionModePlan, got)
	}

	restored, err := client.LoadSession(context.Background(), session.GetID())
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if got := restored.GetPermissionMode(); got != types.PermissionModeOnRequest {
		t.Fatalf("expected restored approval mode %q, got %q", types.PermissionModeOnRequest, got)
	}
	if got := restored.GetExecutionMode(); got != ExecutionModePlan {
		t.Fatalf("expected restored execution mode %q, got %q", ExecutionModePlan, got)
	}
	if restored.GetPermissionContext().PrePlanMode != types.PermissionModeOnRequest {
		t.Fatalf("expected restored pre-plan mode %q, got %q", types.PermissionModeOnRequest, restored.GetPermissionContext().PrePlanMode)
	}

	secondResponse, err := restored.SubmitMessage(context.Background(), "now exit plan mode")
	if err != nil {
		t.Fatalf("second SubmitMessage failed: %v", err)
	}
	if last := secondResponse.Messages[len(secondResponse.Messages)-1]; extractTextContent(last) != "exited plan mode" {
		t.Fatalf("expected second final text %q, got %q", "exited plan mode", extractTextContent(last))
	}
	if got := restored.GetPermissionMode(); got != types.PermissionModeOnRequest {
		t.Fatalf("expected session to restore default mode after ExitPlanMode, got %q", got)
	}
	if got := restored.GetExecutionMode(); got != ExecutionModeExecute {
		t.Fatalf("expected session to restore execution mode %q after ExitPlanMode, got %q", ExecutionModeExecute, got)
	}
	if requests != 4 {
		t.Fatalf("expected 4 provider requests, got %d", requests)
	}
}

// ---------------------------------------------------------------------------
// Priority 3: Memory service fail-fast / explicit fallback
// ---------------------------------------------------------------------------

func TestNewClientMemoryDisabled_MemoryInitErrorIsNil(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		EnableMemory:    false,
		PersistSessions: false,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if got := client.MemoryInitError(); got != nil {
		t.Fatalf("expected nil MemoryInitError when memory disabled, got %v", got)
	}
}

func TestDefaultClientConfig_MemoryFailFastIsFalse(t *testing.T) {
	cfg := DefaultClientConfig()
	if cfg.MemoryFailFast {
		t.Fatal("MemoryFailFast must default to false so clients continue without memory on init failure")
	}
}

func TestNewClientWithMemoryEnabled_DoesNotFailWhenMemoryInitSucceeds(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		EnableMemory:    true,
		MemoryFailFast:  false,
		PersistSessions: false,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	// When memory init succeeds, the init error must be nil.
	if initErr := client.MemoryInitError(); initErr != nil {
		// Memory init failure is acceptable in CI without full env setup;
		// but the client must still be returned without error.
		t.Logf("memory init failed in this environment (acceptable): %v", initErr)
	}
}

func TestAskCompletesAutoModeMonoRun(t *testing.T) {
	requests := 0
	classifierCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		stream, _ := payload["stream"].(bool)
		if !stream {
			classifierCalls++
			if !sdkPayloadContainsText(payload, "run the sdk stub") {
				t.Fatalf("expected classifier transcript to include user request, payload=%#v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(types.APIResponse{
				ID:   "classifier-response",
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					types.TextContent{Text: "<block>false</block><reason>safe</reason>"},
				},
				StopReason: types.StopReasonEndTurn,
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected sdk_stub in tool surface, payload=%#v", payload)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "toolu_auto_1",
						"name":  "sdk_stub",
						"input": map[string]any{},
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"value":"x"}`,
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		case 3:
			if got := sdkCountToolResultsInPayload(payload); got != 1 {
				t.Fatalf("expected 1 tool_result block in final provider request, got %d", got)
			}
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "done",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		default:
			t.Fatalf("unexpected provider request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions: false,
		PermissionMode:  types.PermissionModeAuto,
		AutoCompact:     false,
		// Title generation now fires as soon as the first message is
		// persisted (see session.go), not after the turn completes, so it
		// has time to actually reach this test's mock server before the
		// test tears down - and this test's handler asserts an exact,
		// ordinal request sequence for the tool-call flow under test, with
		// no slot for an unrelated title request. Same pattern as the
		// other DisableTitleGeneration: true tests in this file.
		DisableTitleGeneration: true,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	response, err := client.Ask(context.Background(), "run the sdk stub", []tool.Tool{sdkMonoRunTestTool{}})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if response.Content != "done" {
		t.Fatalf("expected final content %q, got %q", "done", response.Content)
	}
	if len(response.ToolUses) != 1 || len(response.ToolResults) != 1 {
		t.Fatalf("expected 1 tool use/result, got %d/%d", len(response.ToolUses), len(response.ToolResults))
	}
	if classifierCalls != 1 {
		t.Fatalf("expected exactly 1 classifier request, got %d", classifierCalls)
	}
	if requests != 3 {
		t.Fatalf("expected 3 total provider requests (model/classifier/model), got %d", requests)
	}
}

type persistedNativeProviderCase struct {
	provider           types.APIProvider
	model              string
	writeToolTurn      func(t *testing.T, w http.ResponseWriter)
	writeToolResultAck func(t *testing.T, w http.ResponseWriter, payload map[string]any)
	writeResumedTurn   func(t *testing.T, w http.ResponseWriter, payload map[string]any)
}

func testPersistedSessionResumeNativeProvider(t *testing.T, tc persistedNativeProviderCase) {
	t.Helper()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if tc.provider == types.APIProviderGemini && !strings.Contains(r.URL.String(), ":streamGenerateContent?alt=sse") {
			t.Fatalf("expected gemini stream endpoint, got %s", r.URL.String())
		}

		switch requests {
		case 1:
			if !sdkProviderPayloadHasTool(payload, "sdk_stub") {
				t.Fatalf("expected native payload to expose sdk_stub, payload=%#v", payload)
			}
			tc.writeToolTurn(t, w)
		case 2:
			tc.writeToolResultAck(t, w, payload)
		case 3:
			tc.writeResumedTurn(t, w, payload)
		default:
			t.Fatalf("unexpected provider request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClient(&ClientConfig{
		PersistSessions:        true,
		SessionStorageDir:      filepath.Join(t.TempDir(), "sessions"),
		PermissionMode:         types.PermissionModeBypass,
		AutoCompact:            false,
		DisableTitleGeneration: true,
		Model: types.ModelIdentifier{
			Provider: tc.provider,
			Model:    tc.model,
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if err := client.RegisterTool(sdkMonoRunTestTool{}); err != nil {
		t.Fatalf("RegisterTool failed: %v", err)
	}

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: tc.provider,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	firstResponse, err := session.SubmitMessage(context.Background(), "first")
	if err != nil {
		t.Fatalf("first SubmitMessage failed: %v", err)
	}
	if firstResponse.TurnNumber != 1 {
		t.Fatalf("expected first turn number 1, got %d", firstResponse.TurnNumber)
	}
	sessionID := session.GetID()

	restored, err := client.LoadSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	secondResponse, err := restored.SubmitMessage(context.Background(), "second")
	if err != nil {
		t.Fatalf("second SubmitMessage failed: %v", err)
	}
	if secondResponse.TurnNumber != 2 {
		t.Fatalf("expected resumed turn number 2, got %d", secondResponse.TurnNumber)
	}
	if len(secondResponse.Messages) <= len(firstResponse.Messages) {
		t.Fatalf("expected resumed session transcript to grow, got %d then %d messages", len(firstResponse.Messages), len(secondResponse.Messages))
	}
	sessionInfos, err := client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessionInfos) != 1 {
		t.Fatalf("expected 1 persisted session, got %d", len(sessionInfos))
	}
	if sessionInfos[0].ID != sessionID {
		t.Fatalf("expected persisted session ID %q, got %q", sessionID, sessionInfos[0].ID)
	}
	if last := secondResponse.Messages[len(secondResponse.Messages)-1]; extractTextContent(last) != "second done" {
		t.Fatalf("expected resumed final assistant text %q, got %q", "second done", extractTextContent(last))
	}
}

func TestCreateSessionCanBeLoadedBeforeFirstMessage(t *testing.T) {
	client, err := NewClient(&ClientConfig{
		PersistSessions:   true,
		SessionStorageDir: filepath.Join(t.TempDir(), "sessions"),
		PermissionMode:    types.PermissionModeBypass,
		AutoCompact:       false,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderAnthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	restored, err := client.LoadSession(context.Background(), session.GetID())
	if err != nil {
		t.Fatalf("LoadSession immediately after CreateSession failed: %v", err)
	}
	if got := restored.GetStatus(); got != SessionStatusActive {
		t.Fatalf("expected restored session status %q, got %q", SessionStatusActive, got)
	}
	if len(restored.GetMessages()) != 0 {
		t.Fatalf("expected a fresh restored session to have no messages, got %d", len(restored.GetMessages()))
	}
}

func sdkWriteStreamEvents(t *testing.T, w http.ResponseWriter, events []map[string]any) {
	t.Helper()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal stream event: %v", err)
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			t.Fatalf("write stream event: %v", err)
		}
	}
}

func sdkWriteJSONL(t *testing.T, w http.ResponseWriter, events []map[string]any) {
	t.Helper()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal jsonl event: %v", err)
		}
		if _, err := fmt.Fprintf(w, "%s\n", payload); err != nil {
			t.Fatalf("write jsonl event: %v", err)
		}
	}
}

func sdkProviderPayloadHasTool(payload map[string]any, want string) bool {
	rawTools, ok := payload["tools"].([]any)
	if !ok {
		return false
	}
	for _, rawTool := range rawTools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := toolMap["name"].(string); name == want {
			return true
		}
		functionMap, ok := toolMap["function"].(map[string]any)
		if !ok {
			rawDeclarations, ok := toolMap["functionDeclarations"].([]any)
			if !ok {
				continue
			}
			for _, rawDeclaration := range rawDeclarations {
				declaration, ok := rawDeclaration.(map[string]any)
				if !ok {
					continue
				}
				if name, _ := declaration["name"].(string); name == want {
					return true
				}
			}
			continue
		}
		if name, _ := functionMap["name"].(string); name == want {
			return true
		}
	}
	return false
}

func sdkCountToolResultsInPayload(payload map[string]any) int {
	rawMessages, ok := payload["messages"].([]any)
	if ok {
		total := 0
		for _, rawMessage := range rawMessages {
			messageMap, ok := rawMessage.(map[string]any)
			if !ok {
				continue
			}
			if role, _ := messageMap["role"].(string); role == "tool" {
				total++
				continue
			}
			if parts, ok := messageMap["parts"].([]any); ok {
				for _, rawPart := range parts {
					partMap, ok := rawPart.(map[string]any)
					if !ok {
						continue
					}
					if _, ok := partMap["functionResponse"].(map[string]any); ok {
						total++
					}
				}
				continue
			}
			rawContent, ok := messageMap["content"].([]any)
			if !ok {
				continue
			}
			for _, rawBlock := range rawContent {
				blockMap, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				if blockType, _ := blockMap["type"].(string); blockType == string(types.ContentTypeToolResult) {
					total++
				}
			}
		}
		return total
	}

	rawContents, ok := payload["contents"].([]any)
	if !ok {
		return 0
	}
	total := 0
	for _, rawContent := range rawContents {
		contentMap, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := contentMap["parts"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			partMap, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := partMap["functionResponse"].(map[string]any); ok {
				total++
			}
		}
	}
	return total
}

func sdkPayloadContainsText(payload map[string]any, want string) bool {
	if rawMessages, ok := payload["messages"].([]any); ok {
		for _, rawMessage := range rawMessages {
			messageMap, ok := rawMessage.(map[string]any)
			if !ok {
				continue
			}
			if content, _ := messageMap["content"].(string); strings.Contains(content, want) {
				return true
			}
			if rawContent, ok := messageMap["content"].([]any); ok {
				for _, rawBlock := range rawContent {
					blockMap, ok := rawBlock.(map[string]any)
					if !ok {
						continue
					}
					if text, _ := blockMap["text"].(string); strings.Contains(text, want) {
						return true
					}
					if text, _ := blockMap["content"].(string); strings.Contains(text, want) {
						return true
					}
				}
			}
		}
	}
	if rawContents, ok := payload["contents"].([]any); ok {
		for _, rawContent := range rawContents {
			contentMap, ok := rawContent.(map[string]any)
			if !ok {
				continue
			}
			parts, ok := contentMap["parts"].([]any)
			if !ok {
				continue
			}
			for _, rawPart := range parts {
				partMap, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				if text, _ := partMap["text"].(string); strings.Contains(text, want) {
					return true
				}
			}
		}
	}
	return false
}

func TestSessionAutoTitleGeneration(t *testing.T) {
	requestsChan := make(chan string, 2)
	titleModelChan := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}

		systemPrompt, _ := payload["system"].(string)
		if strings.Contains(systemPrompt, "short titles for conversations") {
			if model, _ := payload["model"].(string); model != "" {
				titleModelChan <- model
			}
			requestsChan <- "title"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "Greeting"
					}
				],
				"stop_reason": "end_turn",
				"id": "msg-title"
			}`))
		} else {
			requestsChan <- "message"
			w.Header().Set("Content-Type", "text/event-stream")
			sdkWriteStreamEvents(t, w, []map[string]any{
				{
					"type": "content_block_start",
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				{
					"type": "content_block_delta",
					"delta": map[string]any{
						"type": "text_delta",
						"text": "Hello there!",
					},
				},
				{"type": "content_block_stop"},
				{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
				},
				{"type": "message_stop"},
			})
		}
	}))
	defer server.Close()

	titleCalled := make(chan string, 1)

	client, err := NewClient(&ClientConfig{
		PersistSessions:   true,
		SessionStorageDir: filepath.Join(t.TempDir(), "sessions"),
		PermissionMode:    types.PermissionModeBypass,
		AutoCompact:       false,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderAnthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
		TitleModel: types.ModelIdentifier{
			Provider: types.APIProviderAnthropic,
			Model:    "claude-3-5-haiku-20241022",
		},
		OnSessionTitled: func(id SessionID, title string) {
			titleCalled <- title
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Submit first message, which should trigger title generation after it finishes
	_, err = session.SubmitMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SubmitMessage failed: %v", err)
	}

	// Wait for OnSessionTitled to be called
	select {
	case title := <-titleCalled:
		if title != "Greeting" {
			t.Errorf("expected title to be 'Greeting', got %q", title)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnSessionTitled callback")
	}
	select {
	case model := <-titleModelChan:
		if model != "claude-3-5-haiku-20241022" {
			t.Errorf("expected title model claude-3-5-haiku-20241022, got %q", model)
		}
	default:
		t.Fatal("title request did not record a model")
	}

	// Verify the stored session has the title updated
	metadata, err := client.GetSessionStore().LoadSession(types.SessionID(session.GetID()))
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if metadata.Title != "Greeting" {
		t.Errorf("expected stored title to be 'Greeting', got %q", metadata.Title)
	}
}

func TestSessionAutoTitleGenerationUsesLocalGenerator(t *testing.T) {
	requestsChan := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsChan <- "message"
		w.Header().Set("Content-Type", "text/event-stream")
		sdkWriteStreamEvents(t, w, []map[string]any{
			{
				"type": "content_block_start",
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type": "text_delta",
					"text": "Hello there!",
				},
			},
			{"type": "content_block_stop"},
			{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
			},
			{"type": "message_stop"},
		})
	}))
	defer server.Close()

	titleCalled := make(chan string, 1)
	titleInput := make(chan string, 1)
	client, err := NewClient(&ClientConfig{
		PersistSessions:   true,
		SessionStorageDir: filepath.Join(t.TempDir(), "sessions"),
		PermissionMode:    types.PermissionModeBypass,
		AutoCompact:       false,
		Model: types.ModelIdentifier{
			Provider: types.APIProviderAnthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
		TitleGenerator: fakeTitleGenerator{title: "Titre local", calls: titleInput},
		OnSessionTitled: func(id SessionID, title string) {
			titleCalled <- title
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	apiClient := providers.NewClientWithConfig("test-key", &providers.Config{
		Provider: types.APIProviderAnthropic,
		BaseURL:  server.URL,
	})
	apiClient.SetHTTPClient(server.Client())
	apiClient.SetRetryConfig(types.RetryConfig{MaxAttempts: 1})
	client.queryEngine.SetAPIClient(apiClient)

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err = session.SubmitMessage(context.Background(), "analyse ce document"); err != nil {
		t.Fatalf("SubmitMessage failed: %v", err)
	}

	select {
	case got := <-titleInput:
		if got != "analyse ce document" {
			t.Fatalf("unexpected title input: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for local title generator")
	}
	select {
	case title := <-titleCalled:
		if title != "Titre local" {
			t.Errorf("expected local title, got %q", title)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnSessionTitled callback")
	}
	select {
	case <-requestsChan:
	default:
		t.Fatal("expected one main provider request")
	}
	select {
	case <-requestsChan:
		t.Fatal("unexpected second provider request; title should be local")
	default:
	}
}

func TestDeleteSessionRemovesItsDirectoryAndGrants(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESHAT_RUNTIME_ROOT", root)

	client, err := NewClient(&ClientConfig{
		PersistSessions:        true,
		SessionStorageDir:      filepath.Join(root, "data", "sessions"),
		DisableTitleGeneration: true,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	session, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	id := string(session.GetID())

	plan := filepath.Join(runtimepath.SessionPlansDir("", id), "plan.md")
	grants := runtimepath.SessionPermissionsPath("", id)
	for _, path := range []string{plan, grants} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := client.DeleteSession(session.GetID()); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	for _, path := range []string{filepath.Dir(plan), grants} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed with the session (err=%v)", path, err)
		}
	}
}
