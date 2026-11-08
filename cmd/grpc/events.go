package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
)

// eventBufferSize is how many events a session keeps for a subscriber that
// arrives late or reconnects. A subscriber further behind than this starts from
// the oldest event still held and sees a gap in Sequence, which it can report.
const eventBufferSize = 512

// subscriberBuffer is how many events a subscriber may lag by before the hub
// stops feeding it. The hub never blocks a turn for a slow reader: a client
// that falls behind detects the gap in Sequence and resubscribes from where it
// was.
const subscriberBuffer = 256

// eventHub fans the events of one session out to its subscribers and keeps the
// most recent ones for replay.
type eventHub struct {
	mu       sync.Mutex
	sequence uint64
	buffer   []*pb.SessionEvent
	subs     map[uint64]chan *pb.SessionEvent
	nextSub  uint64
	closed   bool
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[uint64]chan *pb.SessionEvent)}
}

// publish numbers an event, keeps it and hands it to every subscriber. The
// event it returns carries its final sequence and identifier.
func (h *eventHub) publish(event *pb.SessionEvent) *pb.SessionEvent {
	if event == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.sequence++
	event.Sequence = h.sequence
	if event.Timestamp == nil {
		event.Timestamp = timestamppb.Now()
	}
	if event.EventId == "" {
		event.EventId = fmt.Sprintf("%s:%d", event.GetSessionId(), h.sequence)
	}
	h.buffer = append(h.buffer, event)
	if len(h.buffer) > eventBufferSize {
		h.buffer = h.buffer[len(h.buffer)-eventBufferSize:]
	}
	for _, ch := range h.subs {
		select {
		case ch <- event:
		default:
			// The reader is behind; it sees the missing sequences and can
			// subscribe again from its last one.
		}
	}
	return event
}

// subscribe returns the buffered events that follow fromSequence, then a
// channel of the events that come next, and the function that releases the
// subscription.
func (h *eventHub) subscribe(fromSequence uint64) ([]*pb.SessionEvent, <-chan *pb.SessionEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	channel := make(chan *pb.SessionEvent, subscriberBuffer)
	replay := make([]*pb.SessionEvent, 0, len(h.buffer))
	for _, event := range h.buffer {
		if event.Sequence > fromSequence {
			replay = append(replay, event)
		}
	}

	if h.closed {
		close(channel)
		return replay, channel, func() {}
	}

	id := h.nextSub
	h.nextSub++
	h.subs[id] = channel

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if ch, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
		}
	}
	return replay, channel, cancel
}

// close ends every subscription. A subscriber's range over the channel then
// ends, which is how a stream learns the session is gone.
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// lastSequence is the sequence number of the newest event, 0 when none.
func (h *eventHub) lastSequence() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sequence
}

// structFromAnyJSON renders a whole value as JSON and converts it, for the
// events this contract does not name: an older client still receives their
// content in `raw` instead of losing them.
func structFromAnyJSON(value any) *structpb.Struct {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return nil
	}
	out, err := structpb.NewStruct(generic)
	if err != nil {
		return nil
	}
	return out
}

// eventTypesHandledByInteraction are events the server renders itself: the
// prompt bridge emits permission_request and prompt_request with the request id
// a client needs to answer, so the engine's own copies would be duplicates a
// host could not answer. turn.completed and turn.failed are emitted by the
// runner with the full result of the turn.
var eventTypesHandledByInteraction = map[internaltypes.RuntimeEventType]bool{
	internaltypes.RuntimeEventTypeToolPermissionRequired: true,
	internaltypes.RuntimeEventTypePromptRequired:         true,
	internaltypes.RuntimeEventTypeTurnCompleted:          true,
	internaltypes.RuntimeEventTypeTurnFailed:             true,
}

// sessionEventFromRuntime translates one engine event into the wire envelope.
// It returns nil for the events listed above.
func sessionEventFromRuntime(event internaltypes.RuntimeEvent, sessionID, model string) *pb.SessionEvent {
	if eventTypesHandledByInteraction[event.Type] {
		return nil
	}

	out := &pb.SessionEvent{
		SessionId:      sessionID,
		TurnId:         string(event.TurnID),
		TurnNumber:     int32(event.TurnNumber),
		Type:           string(event.Type),
		AgentToolUseId: event.AgentToolUseID,
		Timestamp:      timestampFrom(event.Timestamp),
	}

	switch event.Type {
	case internaltypes.RuntimeEventTypeTurnStarted:
		out.Payload = &pb.SessionEvent_TurnStarted{TurnStarted: &pb.TurnStarted{
			TurnNumber:    int32(event.TurnNumber),
			TurnId:        string(event.TurnID),
			Model:         model,
			ExecutionMode: event.ExecutionMode,
		}}
	case internaltypes.RuntimeEventTypeResponseChunk:
		if event.Chunk != nil {
			out.Payload = &pb.SessionEvent_Chunk{Chunk: chunkToProto(event.Chunk)}
		}
	case internaltypes.RuntimeEventTypeToolProgress:
		if event.ToolProgress != nil {
			out.Payload = &pb.SessionEvent_ToolProgress{ToolProgress: toolProgressToProto(event.ToolProgress)}
		}
	case internaltypes.RuntimeEventTypePlanSubmitted:
		if event.PlanEvent != nil {
			out.Payload = &pb.SessionEvent_Plan{Plan: &pb.PlanEvent{
				PlanId:   event.PlanEvent.PlanID,
				Slug:     event.PlanEvent.Slug,
				Filename: event.PlanEvent.Filename,
				Status:   event.PlanEvent.Status,
				Version:  int32(event.PlanEvent.Version),
				Content:  event.PlanEvent.Content,
			}}
		}
	case internaltypes.RuntimeEventTypeTaskChanged:
		if event.TaskEvent != nil {
			out.Payload = &pb.SessionEvent_Task{Task: &pb.TaskEvent{
				TaskId:  event.TaskEvent.TaskID,
				Action:  event.TaskEvent.Action,
				Status:  event.TaskEvent.Status,
				Subject: event.TaskEvent.Subject,
			}}
		}
	case internaltypes.RuntimeEventTypeStage:
		if event.StageEvent != nil {
			out.Payload = &pb.SessionEvent_Stage{Stage: &pb.StageEvent{
				Stage: event.StageEvent.Stage,
				Label: event.StageEvent.Label,
			}}
		}
	case internaltypes.RuntimeEventTypeProviderRetry:
		if event.ProviderRetryEvent != nil {
			out.Payload = &pb.SessionEvent_ProviderRetry{ProviderRetry: &pb.ProviderRetry{
				Attempt:     int32(event.ProviderRetryEvent.Attempt),
				MaxAttempts: int32(event.ProviderRetryEvent.MaxAttempts),
				WaitMs:      event.ProviderRetryEvent.WaitMs,
				Reason:      event.ProviderRetryEvent.Reason,
			}}
		}
	case internaltypes.RuntimeEventTypeCompacting:
		out.Payload = &pb.SessionEvent_Compaction{Compaction: &pb.CompactionEvent{Started: true}}
	case internaltypes.RuntimeEventTypeCompaction:
		compaction := &pb.CompactionEvent{}
		if event.CompactionEvent != nil {
			compaction.PreCompactTokens = int32(event.CompactionEvent.PreCompactTokens)
			compaction.PostCompactTokens = int32(event.CompactionEvent.PostCompactTokens)
		}
		out.Payload = &pb.SessionEvent_Compaction{Compaction: compaction}
	case internaltypes.RuntimeEventTypeContextUsage:
		if event.ContextUsageEvent != nil {
			out.Payload = &pb.SessionEvent_ContextUsage{ContextUsage: &pb.ContextUsage{
				UsedTokens:          int32(event.ContextUsageEvent.UsedTokens),
				WindowTokens:        int32(event.ContextUsageEvent.WindowTokens),
				AutoCompactAtTokens: int32(event.ContextUsageEvent.AutoCompactAtTokens),
				BlockingLimitTokens: int32(event.ContextUsageEvent.BlockingLimitTokens),
				Source:              event.ContextUsageEvent.Source,
			}}
		}
	case internaltypes.RuntimeEventTypeGoalUpdated:
		if event.GoalEvent != nil {
			goal := &pb.GoalEvent{
				SessionId:       event.GoalEvent.SessionID,
				Objective:       event.GoalEvent.Objective,
				Status:          event.GoalEvent.Status,
				TokensUsed:      event.GoalEvent.TokensUsed,
				TimeUsedSeconds: event.GoalEvent.TimeUsedSeconds,
				CreatedAtMs:     event.GoalEvent.CreatedAt,
				UpdatedAtMs:     event.GoalEvent.UpdatedAt,
			}
			if event.GoalEvent.TokenBudget != nil {
				budget := *event.GoalEvent.TokenBudget
				goal.TokenBudget = &budget
			}
			out.Payload = &pb.SessionEvent_Goal{Goal: goal}
		}
	case internaltypes.RuntimeEventTypeBrowserSession, internaltypes.RuntimeEventTypeBrowserAction,
		internaltypes.RuntimeEventTypeBrowserPage, internaltypes.RuntimeEventTypeBrowserSnapshot,
		internaltypes.RuntimeEventTypeBrowserScreenshot:
		if event.Browser != nil {
			out.Payload = &pb.SessionEvent_Browser{Browser: &pb.BrowserEvent{
				Action:        event.Browser.Action,
				PageId:        event.Browser.PageID,
				Url:           event.Browser.URL,
				Title:         event.Browser.Title,
				ActivePageId:  event.Browser.ActivePageID,
				PageCount:     int32(event.Browser.PageCount),
				ActionCount:   int32(event.Browser.ActionCount),
				ElementCount:  int32(event.Browser.ElementCount),
				HeadingCount:  int32(event.Browser.HeadingCount),
				TextLength:    int32(event.Browser.TextLength),
				Bytes:         int64(event.Browser.Bytes),
				PersistedPath: event.Browser.PersistedPath,
				PersistedSize: int64(event.Browser.PersistedSize),
				FullPage:      event.Browser.FullPage,
			}}
		}
	default:
		if isAgentEvent(event.Type) && event.AgentEvent != nil {
			out.Payload = &pb.SessionEvent_Agent{Agent: &pb.AgentEvent{
				EventType:     string(event.Type),
				CallId:        event.AgentEvent.CallID,
				AgentId:       event.AgentEvent.AgentID,
				AgentNickname: event.AgentEvent.AgentNickname,
				AgentRole:     event.AgentEvent.AgentRole,
				Prompt:        event.AgentEvent.Prompt,
				Status:        event.AgentEvent.Status,
				Message:       event.AgentEvent.Message,
				StartedAtMs:   event.AgentEvent.StartedAtMs,
				CompletedAtMs: event.AgentEvent.CompletedAtMs,
			}}
			break
		}
		out.Payload = &pb.SessionEvent_Raw{Raw: &pb.RawEvent{
			Type:  string(event.Type),
			Data:  structFromAnyJSON(event),
			Error: event.Error,
		}}
	}

	// An event with no payload yet still carries its type, so a client can show
	// it rather than receive an empty message it cannot interpret.
	if out.Payload == nil {
		out.Payload = &pb.SessionEvent_Raw{Raw: &pb.RawEvent{
			Type:  string(event.Type),
			Data:  structFromAnyJSON(event),
			Error: event.Error,
		}}
	}
	return out
}

func isAgentEvent(eventType internaltypes.RuntimeEventType) bool {
	switch eventType {
	case internaltypes.RuntimeEventTypeAgentSpawnBegin,
		internaltypes.RuntimeEventTypeAgentSpawnEnd,
		internaltypes.RuntimeEventTypeAgentWaitBegin,
		internaltypes.RuntimeEventTypeAgentWaitEnd,
		internaltypes.RuntimeEventTypeAgentInteractionBegin,
		internaltypes.RuntimeEventTypeAgentInteractionEnd,
		internaltypes.RuntimeEventTypeAgentCloseBegin,
		internaltypes.RuntimeEventTypeAgentCloseEnd:
		return true
	default:
		return false
	}
}
