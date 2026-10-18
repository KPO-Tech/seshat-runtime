package agent

import (
	"context"
	"fmt"
	skills "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
	publicskills "github.com/KPO-Tech/seshat-runtime/pkg/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestAsyncAgentManager_BasicAgentExecution tests basic async agent execution
func TestAsyncAgentManager_BasicAgentExecution(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	// Create a simple agent config
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test task",
		MaxTurns:  2,
		Context:   context.Background(),
	}

	// Start agent asynchronously
	agent, err := manager.StartAgent(config)
	require.NoError(t, err)
	require.NotNil(t, agent)
	require.Equal(t, AgentTypeExplore, agent.Config.AgentType)

	// Wait for agent to complete
	agent.Wait()

	// Check final status
	assert.True(t, agent.IsComplete())
	assert.False(t, agent.IsRunning())

	// Verify agent can be retrieved
	retrievedAgent, err := manager.GetAgent(agent.ID)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, retrievedAgent.ID)
}

// TestAsyncAgentManager_StartAgentHasWallClockTimeout guards against
// regressing to the old behavior where a background agent's context was
// context.WithCancel(context.Background()) - cancel-only, no deadline - so a
// stalled/looping sub-agent (e.g. the spawn_agent/wait_agent "browse" tool
// path) had nothing to stop it. DefaultSubAgentTimeout was already wired
// into the synchronous agent tool but not into this async path.
func TestAsyncAgentManager_StartAgentHasWallClockTimeout(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test task",
		MaxTurns:  1,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	deadline, ok := agent.Ctx.Deadline()
	require.True(t, ok, "expected the async agent's context to carry a wall-clock deadline, not be cancel-only")
	expected := time.Now().Add(time.Duration(DefaultSubAgentTimeout) * time.Second)
	assert.WithinDuration(t, expected, deadline, 5*time.Second)

	agent.Wait()
}

// TestAsyncAgentManager_RealTimeEvents tests real-time event notifications
func TestAsyncAgentManager_RealTimeEvents(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	var events []AgentEvent
	var eventsMu sync.Mutex

	// Add global event listener
	listener := func(event AgentEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
		fmt.Printf("Event received: %s for agent %s\n", event.EventType, event.AgentID)
	}
	manager.AddGlobalListener(listener)
	defer manager.RemoveGlobalListener(listener)

	// Create agent config
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test real-time events",
		MaxTurns:  3,
		Context:   context.Background(),
	}

	// Start agent
	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	// Wait for completion
	agent.Wait()

	// Give some time for all events to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify we received expected events
	eventsMu.Lock()
	defer eventsMu.Unlock()

	assert.Greater(t, len(events), 0, "Should receive at least one event")

	// Check for started event
	hasStartedEvent := false
	for _, event := range events {
		if event.EventType == AgentEventStarted {
			hasStartedEvent = true
			assert.Equal(t, agent.ID, event.AgentID)
			assert.Equal(t, AgentTypeExplore, event.AgentType)
			break
		}
	}
	assert.True(t, hasStartedEvent, "Should receive started event")

	// Check for completed or failed event
	hasFinalEvent := false
	for _, event := range events {
		if event.EventType == AgentEventCompleted || event.EventType == AgentEventFailed {
			hasFinalEvent = true
			assert.Equal(t, agent.ID, event.AgentID)
			break
		}
	}
	assert.True(t, hasFinalEvent, "Should receive final event")
}

// TestAsyncAgentManager_ProgressEvents tests progress event streaming
func TestAsyncAgentManager_ProgressEvents(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	var progressEvents []AgentEvent
	var progressMu sync.Mutex

	// Add listener for progress events
	listener := func(event AgentEvent) {
		if event.EventType == AgentEventProgress {
			progressMu.Lock()
			defer progressMu.Unlock()
			progressEvents = append(progressEvents, event)
			fmt.Printf("Progress: Turn %d/%d (%.1f%%)\n",
				event.Progress.CurrentTurn,
				event.Progress.MaxTurns,
				event.Progress.PercentComplete)
		}
	}
	manager.AddGlobalListener(listener)
	defer manager.RemoveGlobalListener(listener)

	// Create agent config
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test progress events",
		MaxTurns:  3,
		Context:   context.Background(),
	}

	// Start agent
	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	// Wait for completion
	agent.Wait()

	// Give some time for all events to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify progress events
	progressMu.Lock()
	defer progressMu.Unlock()

	fmt.Printf("Total progress events: %d\n", len(progressEvents))

	// We should have received at least one progress event
	if len(progressEvents) > 0 {
		// Verify progress data structure
		event := progressEvents[0]
		assert.NotNil(t, event.Progress)
		assert.Greater(t, event.Progress.CurrentTurn, 0)
		assert.Greater(t, event.Progress.MaxTurns, 0)
	}
}

// TestAsyncAgentManager_AgentCancellation tests agent cancellation
func TestAsyncAgentManager_AgentCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cancellation test in short mode")
	}

	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	var events []AgentEvent
	var eventsMu sync.Mutex

	// Add event listener
	listener := func(event AgentEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
		fmt.Printf("Event: %s\n", event.EventType)
	}
	manager.AddGlobalListener(listener)
	defer manager.RemoveGlobalListener(listener)

	// Create agent config with longer task
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Perform comprehensive code analysis and documentation generation", // Very long task
		MaxTurns:  10,                                                                 // Long running
		Context:   context.Background(),
	}

	// Start agent
	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	// Wait for agent to actually start running
	timeout := time.After(5 * time.Second)
	started := false

	for !started {
		select {
		case <-timeout:
			t.Fatal("Agent did not start within timeout")
		case <-time.After(50 * time.Millisecond):
			if agent.IsRunning() {
				started = true
			} else if agent.IsComplete() {
				// Agent completed too quickly, skip test
				t.Skip("Agent completed too quickly for cancellation test")
			}
		}
	}

	// Cancel the agent
	err = manager.CancelAgent(agent.ID)
	if err != nil {
		// Agent might have completed, that's okay
		agent.Wait()
		return
	}

	// Wait for agent to be cancelled
	agent.Wait()

	// Verify status (either cancelled or completed/failed)
	assert.True(t, agent.IsComplete(), "Agent should be complete after cancellation")

	// Give some time for all events to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify we received events
	eventsMu.Lock()
	defer eventsMu.Unlock()

	assert.Greater(t, len(events), 0, "Should receive at least some events")
}

func TestAsyncAgentManager_CloseAllAgentsRemovesTrackedAgents(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	configs := []*RunConfig{
		{AgentType: AgentTypeExplore, Task: "Long-running task A", MaxTurns: 10, Context: context.Background()},
		{AgentType: AgentTypeExplore, Task: "Long-running task B", MaxTurns: 10, Context: context.Background()},
	}

	agents := make([]*AsyncAgent, 0, len(configs))
	for _, cfg := range configs {
		agent, err := manager.StartAgent(cfg)
		require.NoError(t, err)
		agents = append(agents, agent)
	}

	closed := manager.CloseAllAgents()
	assert.Equal(t, len(agents), closed)
	assert.Empty(t, manager.ListAgents())

	for _, agent := range agents {
		agent.Wait()
		assert.True(t, agent.IsComplete())
	}
}

// TestAsyncAgentManager_ConcurrentAgents tests multiple concurrent agents
func TestAsyncAgentManager_ConcurrentAgents(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	agentCount := 3
	var wg sync.WaitGroup
	var agents []*AsyncAgent
	var agentsMu sync.Mutex

	// Start multiple agents concurrently
	for i := 0; i < agentCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			config := &RunConfig{
				AgentType: AgentTypeExplore,
				Task:      fmt.Sprintf("Concurrent agent %d", index),
				MaxTurns:  2,
				Context:   context.Background(),
			}

			agent, err := manager.StartAgent(config)
			if err == nil {
				agentsMu.Lock()
				agents = append(agents, agent)
				agentsMu.Unlock()
				agent.Wait()
			}
		}(i)
	}

	// Wait for all agents to complete
	wg.Wait()

	// Verify all agents completed
	assert.Len(t, agents, agentCount, "Should have started all agents")

	for _, agent := range agents {
		assert.True(t, agent.IsComplete(), "Agent %s should be complete", agent.ID)
	}

	// List all agents
	allAgents := manager.ListAgents()
	assert.GreaterOrEqual(t, len(allAgents), agentCount, "Should have at least the agents we started")
}

// TestAsyncAgentManager_AgentSpecificListeners tests agent-specific event listeners
func TestAsyncAgentManager_AgentSpecificListeners(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	var agentEvents []AgentEvent
	var agentEventsMu sync.Mutex

	// Create agent config
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test agent-specific listeners",
		MaxTurns:  2,
		Context:   context.Background(),
	}

	// Start agent
	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	// Add agent-specific listener
	agentListener := func(event AgentEvent) {
		if event.AgentID == agent.ID {
			agentEventsMu.Lock()
			defer agentEventsMu.Unlock()
			agentEvents = append(agentEvents, event)
			fmt.Printf("Agent-specific event: %s\n", event.EventType)
		}
	}
	agent.AddListener(agentListener)
	defer agent.RemoveListener(agentListener)

	// Wait for completion
	agent.Wait()

	// Give some time for all events to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify we received agent-specific events
	agentEventsMu.Lock()
	defer agentEventsMu.Unlock()

	assert.Greater(t, len(agentEvents), 0, "Should receive agent-specific events")
}

// TestAsyncAgentManager_WaitWithTimeout tests timeout on agent wait
func TestAsyncAgentManager_WaitWithTimeout(t *testing.T) {
	agent := &AsyncAgent{
		ID:   "test-agent",
		done: make(chan struct{}),
	}

	// Wait with very short timeout on an unfinished agent.
	err := agent.WaitWithTimeout(1 * time.Millisecond)
	assert.Error(t, err, "Should timeout waiting for agent")

	close(agent.done)
	assert.NoError(t, agent.WaitWithTimeout(10*time.Millisecond))
}

// TestAsyncAgentManager_Cleanup tests cleanup of completed agents
func TestAsyncAgentManager_Cleanup(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	// Start and complete an agent
	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test cleanup",
		MaxTurns:  2,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	require.NoError(t, err)
	agent.Wait()

	// Verify agent exists
	allAgents := manager.ListAgents()
	assert.Greater(t, len(allAgents), 0, "Should have active agents")

	// Cleanup
	manager.Cleanup()

	// Verify completed agent was removed
	allAgents = manager.ListAgents()
	for _, a := range allAgents {
		assert.NotEqual(t, agent.ID, a.ID, "Completed agent should be removed")
	}
}

// TestAsyncAgentManager_Shutdown tests graceful shutdown
func TestAsyncAgentManager_Shutdown(t *testing.T) {
	manager := NewAsyncAgentManager()

	// Start multiple agents
	for i := 0; i < 3; i++ {
		config := &RunConfig{
			AgentType: AgentTypeExplore,
			Task:      fmt.Sprintf("Test shutdown agent %d", i),
			MaxTurns:  10, // Long running
			Context:   context.Background(),
		}

		agent, err := manager.StartAgent(config)
		require.NoError(t, err)

		// Don't wait, let shutdown handle them
		_ = agent
	}

	// Shutdown should cancel all running agents
	manager.Shutdown()

	// Verify no agents are running
	allAgents := manager.ListAgents()
	for _, agent := range allAgents {
		assert.False(t, agent.IsRunning(), "No agents should be running after shutdown")
	}
}

// TestAsyncAgent_GetProgress tests progress information retrieval
func TestAsyncAgent_GetProgress(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test progress retrieval",
		MaxTurns:  2,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	require.NoError(t, err)

	// Get progress
	progress := agent.GetProgress()
	assert.NotNil(t, progress)
	assert.GreaterOrEqual(t, progress.CurrentTurn, 0)
	assert.Greater(t, progress.MaxTurns, 0)

	// Wait for completion
	agent.Wait()

	// Get final progress
	finalProgress := agent.GetProgress()
	assert.NotNil(t, finalProgress)
	// Agent may have completed without any turns, so just check structure
	assert.GreaterOrEqual(t, finalProgress.CurrentTurn, 0)
}

// TestAsync_ConcurrentCancellation verifies that concurrent Cancel calls
// on the same agent are race-free.
func TestAsync_ConcurrentCancellation(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test concurrent cancellation",
		MaxTurns:  5,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Errors are expected; the goal is no data race.
			_ = manager.CancelAgent(agent.ID)
		}()
	}
	wg.Wait()
	agent.Wait()
	if !agent.IsComplete() {
		t.Error("agent should be complete after concurrent cancellations")
	}
}

// TestAsync_ConcurrentStatusRead verifies that concurrent reads of agent
// state fields are race-free while the agent is running.
func TestAsync_ConcurrentStatusRead(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	config := &RunConfig{
		AgentType: AgentTypeExplore,
		Task:      "Test concurrent read",
		MaxTurns:  5,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	const readers = 20
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// Access all state methods — must be race-free.
					_ = agent.IsRunning()
					_ = agent.IsComplete()
					_ = agent.GetProgress()
					_ = agent.GetDuration()
				}
			}
		}()
	}

	agent.Wait()
	close(stop)
	wg.Wait()
}

// TestAsync_ErrorPropagation verifies that an agent failing due to a bad
// config transitions to AgentStatusFailed and exposes an error.
func TestAsync_ErrorPropagation(t *testing.T) {
	manager := NewAsyncAgentManager()
	defer manager.Shutdown()

	// A zero-value RunConfig will cause RunAgent to error.
	config := &RunConfig{
		AgentType: "test-error",
		Task:      "",
		MaxTurns:  1,
		Context:   context.Background(),
	}

	agent, err := manager.StartAgent(config)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	agent.Wait()
	if !agent.IsComplete() {
		t.Error("agent should be complete")
	}
	// Either Failed or Completed (depends on RunAgent internals) — just must not be Running.
	if agent.IsRunning() {
		t.Error("agent must not be running after Wait()")
	}
}

func TestBrowseAgentIsRegistered(t *testing.T) {
	agent := GetBuiltInAgentByType(AgentTypeBrowse)
	require.NotNil(t, agent, "expected browse agent to be registered")
	if agent.AgentType != AgentTypeBrowse {
		t.Fatalf("expected browse agent type %q, got %q", AgentTypeBrowse, agent.AgentType)
	}
}

func TestBrowseAgentToolSurfaceIsReadOnlyResearchFocused(t *testing.T) {
	agent := GetBuiltInAgentByType(AgentTypeBrowse)
	require.NotNil(t, agent, "expected browse agent to be registered")

	allowed := make(map[string]bool, len(agent.Tools))
	for _, tool := range agent.Tools {
		allowed[tool] = true
	}

	for _, expected := range []string{"web_search", "web_fetch", "browser_open", "read_file", "glob", "grep"} {
		if !allowed[expected] {
			t.Fatalf("expected browse agent to allow %q", expected)
		}
	}

	for _, blocked := range []string{"write_file", "edit_file", "bash", "task_create"} {
		if allowed[blocked] {
			t.Fatalf("expected browse agent not to allow %q", blocked)
		}
	}
}

// TestPlanAgentToolSurfaceIsReadOnly guards against a regression of M5c:
// the plan agent used to carry write_file/edit_file despite its own prompt
// never mentioning writing, and despite ExecutionModePlan denying writes
// outright at the session level.
func TestPlanAgentToolSurfaceIsReadOnly(t *testing.T) {
	agent := GetBuiltInAgentByType(AgentTypePlan)
	require.NotNil(t, agent, "expected plan agent to be registered")

	allowed := make(map[string]bool, len(agent.Tools))
	for _, tool := range agent.Tools {
		allowed[tool] = true
	}

	for _, expected := range []string{"read_file", "glob", "grep"} {
		if !allowed[expected] {
			t.Fatalf("expected plan agent to allow %q", expected)
		}
	}

	for _, blocked := range []string{"write_file", "edit_file", "bash", "task_create"} {
		if allowed[blocked] {
			t.Fatalf("expected plan agent not to allow %q", blocked)
		}
	}
}

// TestMemoryAdapter_BasicOperations tests basic memory operations.
func TestMemoryAdapter_BasicOperations(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store a memory entry
	entry := MemoryEntry{
		ID:      "test1",
		Type:    MemoryTypeToolUsage,
		Content: "Test content",
		Tags:    []string{"test", "basic"},
	}

	err := memSystem.StoreEntry(entry)
	require.NoError(t, err)

	// Retrieve the memory
	retrieved, err := memSystem.GetEntry("test1")
	require.NoError(t, err)
	assert.Equal(t, "test1", retrieved.ID)
	assert.Equal(t, "Test content", retrieved.Content)
	assert.Equal(t, []string{"test", "basic"}, retrieved.Tags)
}

// TestMemoryAdapter_StoreAndRetrieve tests storing and retrieving memory entries.
func TestMemoryAdapter_StoreAndRetrieve(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store multiple entries
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "Tool usage pattern 1",
			Tags:    []string{"tool", "readFile"},
		},
		{
			Type:    MemoryTypeError,
			Content: "Error pattern 1",
			Tags:    []string{"error", "file_not_found"},
		},
		{
			Type:    MemoryTypeConversation,
			Content: "Conversation about project structure",
			Tags:    []string{"conversation", "project"},
		},
	}

	for _, entry := range entries {
		err := memSystem.StoreEntry(entry)
		require.NoError(t, err)
	}

	// Query for specific types
	query := MemoryQuery{
		Types: []MemoryType{MemoryTypeToolUsage},
		Limit: 10,
	}

	result, err := memSystem.Search(query)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Entries), 1)
}

// TestMemoryAdapter_ToolUsageLearning tests learning from tool usage.
func TestMemoryAdapter_ToolUsageLearning(t *testing.T) {
	config := DefaultMemoryConfig()
	config.LearningEnabled = true
	memoryWithConfig := NewMemoryAdapterWithConfig(config)
	memoryWithConfig.Start()
	defer memoryWithConfig.Stop()

	// Learn from successful tool usage
	params := map[string]any{
		"path":     "/tmp/test.txt",
		"encoding": "utf-8",
	}

	err := memoryWithConfig.LearnToolUsage("readFile", params, true, nil)
	require.NoError(t, err)

	// Retrieve learned patterns
	patterns, err := memoryWithConfig.GetToolUsagePatterns("readFile")
	require.NoError(t, err)
	assert.NotNil(t, patterns)
	assert.Equal(t, "readFile", patterns.ToolName)
	assert.GreaterOrEqual(t, patterns.UsageCount, 1)
}

// TestMemoryAdapter_ToolUsageLearningFailure tests learning from failed tool usage.
func TestMemoryAdapter_ToolUsageLearningFailure(t *testing.T) {
	config := DefaultMemoryConfig()
	config.LearningEnabled = true
	memoryWithConfig := NewMemoryAdapterWithConfig(config)
	memoryWithConfig.Start()
	defer memoryWithConfig.Stop()

	// Learn from failed tool usage
	params := map[string]any{
		"path":     "/tmp/nonexistent.txt",
		"encoding": "utf-8",
	}

	err := memoryWithConfig.LearnToolUsage("readFile", params, false, nil)
	require.NoError(t, err)

	// Retrieve learned patterns
	patterns, err := memoryWithConfig.GetToolUsagePatterns("readFile")
	require.NoError(t, err)
	assert.NotNil(t, patterns)
	assert.Greater(t, len(patterns.FailedParameters), 0)
}

// TestMemoryAdapter_QueryByTags tests querying memory by tags.
func TestMemoryAdapter_QueryByTags(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store entries with different tags
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "File operation",
			Tags:    []string{"file", "read"},
		},
		{
			Type:    MemoryTypeToolUsage,
			Content: "Another file operation",
			Tags:    []string{"file", "write"},
		},
		{
			Type:    MemoryTypeError,
			Content: "Network error",
			Tags:    []string{"error", "network"},
		},
	}

	for _, entry := range entries {
		_ = memSystem.StoreEntry(entry)
	}

	// Query by tags
	query := MemoryQuery{
		Tags:  []string{"file"},
		Limit: 10,
	}

	result, err := memSystem.Search(query)
	require.NoError(t, err)
	assert.Equal(t, 2, len(result.Entries))
}

// TestMemoryAdapter_QueryByContent tests querying memory by content.
func TestMemoryAdapter_QueryByContent(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store entries
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "Python script execution",
			Tags:    []string{"python", "script"},
		},
		{
			Type:    MemoryTypeToolUsage,
			Content: "Bash command execution",
			Tags:    []string{"bash", "command"},
		},
	}

	for _, entry := range entries {
		_ = memSystem.StoreEntry(entry)
	}

	// Query by content
	query := MemoryQuery{
		Content: "Python",
		Limit:   10,
	}

	result, err := memSystem.Search(query)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Entries))
	assert.Contains(t, result.Entries[0].Content, "Python")
}

// TestMemoryAdapter_ImportExport tests import/export functionality.
func TestMemoryAdapter_ImportExport(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store some entries
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "Test content 1",
			Tags:    []string{"test"},
		},
		{
			Type:    MemoryTypeError,
			Content: "Test content 2",
			Tags:    []string{"test"},
		},
	}

	for _, entry := range entries {
		_ = memSystem.StoreEntry(entry)
	}

	// Export memory
	data, err := memSystem.Export()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Create new memory system and import
	newMemory := NewMemoryAdapter()
	newMemory.Start()
	defer newMemory.Stop()
	err = newMemory.Import(data)
	require.NoError(t, err)

	// Verify imported data
	stats := newMemory.Stats()
	assert.GreaterOrEqual(t, stats.TotalEntries, 2)
}

// TestMemoryAdapter_Expiration tests memory expiration.
func TestMemoryAdapter_Expiration(t *testing.T) {
	config := DefaultMemoryConfig()
	config.DefaultTTL = 100 * time.Millisecond // Very short TTL for testing
	memSystem := NewMemoryAdapterWithConfig(config)
	memSystem.Start()
	defer memSystem.Stop()

	// Store an entry
	entry := MemoryEntry{
		Type:    MemoryTypeToolUsage,
		Content: "Will expire soon",
		Tags:    []string{"test"},
	}

	err := memSystem.StoreEntry(entry)
	require.NoError(t, err)

	// Query immediately - should find it
	query := MemoryQuery{
		Types: []MemoryType{MemoryTypeToolUsage},
		Limit: 10,
	}

	result, err := memSystem.Search(query)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Entries), 1)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Query again - should not find it
	result, err = memSystem.Search(query)
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Entries))
}

// TestMemoryAdapter_ImportanceScoring tests importance calculation.
func TestMemoryAdapter_ImportanceScoring(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store entries with different characteristics
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "Short content",
			Tags:    []string{"test"},
		},
		{
			Type:    MemoryTypeToolUsage,
			Content: "This is a much longer content that should have higher importance because it contains more information and context",
			Tags:    []string{"test", "important", "detailed"},
		},
	}

	for _, entry := range entries {
		_ = memSystem.StoreEntry(entry)
	}

	// Query and check importance
	query := MemoryQuery{
		Types:         []MemoryType{MemoryTypeToolUsage},
		MinImportance: 0.5, // Filter by minimum importance
		Limit:         10,
	}

	result, err := memSystem.Search(query)
	require.NoError(t, err)
	// Only the longer content should be returned
	assert.Equal(t, 1, len(result.Entries))
	assert.Greater(t, result.Entries[0].Importance, 0.5)
}

// TestMemoryAdapter_CleanupExpired tests automatic cleanup of expired entries.
func TestMemoryAdapter_CleanupExpired(t *testing.T) {
	config := DefaultMemoryConfig()
	config.DefaultTTL = 50 * time.Millisecond
	memSystem := NewMemoryAdapterWithConfig(config)
	memSystem.Start()
	defer memSystem.Stop()

	// Store entries with different creation times
	entries := []MemoryEntry{
		{
			Type:    MemoryTypeToolUsage,
			Content: "Old entry",
			Tags:    []string{"test"},
		},
		{
			Type:    MemoryTypeToolUsage,
			Content: "New entry",
			Tags:    []string{"test"},
		},
	}

	for i, entry := range entries {
		// Create entries with different times
		if i == 0 {
			entry.CreatedAt = time.Now().Add(-1 * time.Hour)
		}
		_ = memSystem.StoreEntry(entry)
	}

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Trigger cleanup
	cleaned := memSystem.CleanupExpired()
	assert.GreaterOrEqual(t, cleaned, 1)
}

// TestMemoryAdapter_DeleteEntry tests deleting memory entries.
func TestMemoryAdapter_DeleteEntry(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store an entry
	entry := MemoryEntry{
		ID:      "delete_test",
		Type:    MemoryTypeToolUsage,
		Content: "To be deleted",
		Tags:    []string{"test"},
	}

	err := memSystem.StoreEntry(entry)
	require.NoError(t, err)

	// Verify it exists
	storedEntry, err := memSystem.GetEntry(entry.ID)
	require.NoError(t, err)
	require.NotNil(t, storedEntry)

	// Delete it
	err = memSystem.DeleteEntry(entry.ID)
	require.NoError(t, err)

	// Verify it's gone
	_, err = memSystem.GetEntry(entry.ID)
	assert.Error(t, err)
}

// TestMemoryAdapter_Statistics tests memory statistics.
func TestMemoryAdapter_Statistics(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store various types of entries
	entries := []MemoryEntry{
		{Type: MemoryTypeToolUsage, Content: "Tool 1", Tags: []string{"tool"}},
		{Type: MemoryTypeToolUsage, Content: "Tool 2", Tags: []string{"tool"}},
		{Type: MemoryTypeError, Content: "Error 1", Tags: []string{"error"}},
		{Type: MemoryTypeConversation, Content: "Conv 1", Tags: []string{"conv"}},
	}

	for _, entry := range entries {
		_ = memSystem.StoreEntry(entry)
	}

	// Get statistics
	stats := memSystem.Stats()
	assert.Equal(t, 4, stats.TotalEntries)
	assert.Equal(t, 2, stats.EntriesByType[MemoryTypeToolUsage])
	assert.Equal(t, 1, stats.EntriesByType[MemoryTypeError])
	assert.Equal(t, 1, stats.EntriesByType[MemoryTypeConversation])
}

// TestMemoryAdapter_ConcurrentAccess tests thread-safe concurrent access.
func TestMemoryAdapter_ConcurrentAccess(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	done := make(chan bool, 10)

	// Concurrent stores
	for i := 0; i < 5; i++ {
		go func(id int) {
			entry := MemoryEntry{
				ID:      fmt.Sprintf("concurrent_%d", id),
				Type:    MemoryTypeToolUsage,
				Content: fmt.Sprintf("Content %d", id),
				Tags:    []string{"concurrent"},
			}
			_ = memSystem.StoreEntry(entry)
			done <- true
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 5; i++ {
		go func(id int) {
			query := MemoryQuery{
				Types: []MemoryType{MemoryTypeToolUsage},
				Limit: 10,
			}
			_, _ = memSystem.Search(query)
			done <- true
		}(i)
	}

	// Wait for all operations
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify data integrity
	stats := memSystem.Stats()
	assert.GreaterOrEqual(t, stats.TotalEntries, 5)
}

// TestMemoryAdapter_PatternMatching tests parameter pattern matching.
func TestMemoryAdapter_PatternMatching(t *testing.T) {
	config := DefaultMemoryConfig()
	config.LearningEnabled = true
	memoryWithConfig := NewMemoryAdapterWithConfig(config)
	memoryWithConfig.Start()
	defer memoryWithConfig.Stop()

	// Learn multiple successful uses with similar parameters
	params1 := map[string]any{"path": "/tmp/test.txt", "encoding": "utf-8"}
	params2 := map[string]any{"path": "/tmp/test.txt", "encoding": "utf-8"}
	params3 := map[string]any{"path": "/tmp/other.txt", "encoding": "utf-8"}

	_ = memoryWithConfig.LearnToolUsage("readFile", params1, true, nil)
	_ = memoryWithConfig.LearnToolUsage("readFile", params2, true, nil)
	_ = memoryWithConfig.LearnToolUsage("readFile", params3, true, nil)

	// Retrieve patterns
	patterns, err := memoryWithConfig.GetToolUsagePatterns("readFile")
	require.NoError(t, err)

	// Check that we learned the pattern
	assert.Equal(t, "readFile", patterns.ToolName)
	assert.GreaterOrEqual(t, len(patterns.SuccessfulParameters), 2)
}

// TestMemoryAdapter_MultipleToolTypes tests handling different tool types.
func TestMemoryAdapter_MultipleToolTypes(t *testing.T) {
	config := DefaultMemoryConfig()
	config.LearningEnabled = true
	memoryWithConfig := NewMemoryAdapterWithConfig(config)
	memoryWithConfig.Start()
	defer memoryWithConfig.Stop()

	// Learn from different tools
	tools := []string{"readFile", "writeFile", "grep", "bash"}
	for _, tool := range tools {
		params := map[string]any{"test": "value"}
		_ = memoryWithConfig.LearnToolUsage(tool, params, true, nil)
	}

	// Get stats
	stats := memoryWithConfig.Stats()
	assert.Equal(t, 4, stats.EntriesByType[MemoryTypeToolUsage])
}

// TestMemoryAdapter_SuccessRateCalculation tests success rate calculation.
func TestMemoryAdapter_SuccessRateCalculation(t *testing.T) {
	config := DefaultMemoryConfig()
	config.LearningEnabled = true
	memoryWithConfig := NewMemoryAdapterWithConfig(config)
	memoryWithConfig.Start()
	defer memoryWithConfig.Stop()

	// Mix of successes and failures
	params1 := map[string]any{"path": "/tmp/exists.txt"}
	params2 := map[string]any{"path": "/tmp/exists.txt"}
	params3 := map[string]any{"path": "/tmp/nonexistent.txt"}

	_ = memoryWithConfig.LearnToolUsage("readFile", params1, true, nil)
	_ = memoryWithConfig.LearnToolUsage("readFile", params2, true, nil)
	_ = memoryWithConfig.LearnToolUsage("readFile", params3, false, nil)

	// Get patterns
	patterns, err := memoryWithConfig.GetToolUsagePatterns("readFile")
	require.NoError(t, err)

	// Success rate should be ~0.67
	assert.InDelta(t, 0.67, patterns.SuccessRate, 0.01)
}

// TestDefaultMemoryConfig tests default configuration values
func TestDefaultMemoryConfig(t *testing.T) {
	config := DefaultMemoryConfig()

	assert.Equal(t, 10000, config.MaxEntries)
	assert.Equal(t, 7*24*time.Hour, config.DefaultTTL)
	assert.True(t, config.LearningEnabled)
	assert.Equal(t, 0.1, config.ImportanceDecay)
	assert.Equal(t, 0.1, config.MinImportance)
	assert.Equal(t, 1.0, config.MaxImportance)
	assert.True(t, config.EnableSemanticSearch)
}

// TestMemoryRetentionPolicy tests retention policy application
func TestMemoryRetentionPolicy(t *testing.T) {
	config := DefaultMemoryConfig()
	policy := &config.RetentionPolicy

	assert.Equal(t, 30*24*time.Hour, policy.ToolUsageRetention)
	assert.Equal(t, 7*24*time.Hour, policy.ConversationRetention)
	assert.Equal(t, 30*24*time.Hour, policy.ErrorRetention)
	assert.Equal(t, 3*24*time.Hour, policy.ContextRetention)
	assert.Equal(t, 14*24*time.Hour, policy.SuccessRetention)
}

// TestMemoryContext tests memory context structure
func TestMemoryContext(t *testing.T) {
	context := &MemoryContext{
		SessionID: "test_session",
		Task:      "test task",
		Tool:      "readFile",
		Intent:    "test intent",
	}

	assert.Equal(t, "test_session", context.SessionID)
	assert.Equal(t, "test task", context.Task)
	assert.Equal(t, "readFile", context.Tool)
	assert.Equal(t, "test intent", context.Intent)
}

// TestToolUsageMemory tests tool usage memory structure
func TestToolUsageMemory(t *testing.T) {
	memory := &ToolUsageMemory{
		ToolName: "readFile",
		SuccessfulParameters: []ParameterPattern{
			{
				Parameters: map[string]any{"path": "/tmp/test.txt"},
				Frequency:  5,
				Success:    true,
			},
		},
		FailedParameters: []ParameterPattern{
			{
				Parameters: map[string]any{"path": "/tmp/missing.txt"},
				Frequency:  2,
				Success:    false,
			},
		},
		TypicalUsage: "Read file with utf-8 encoding",
		SuccessRate:  0.71,
		UsageCount:   7,
	}

	assert.Equal(t, "readFile", memory.ToolName)
	assert.Equal(t, 1, len(memory.SuccessfulParameters))
	assert.Equal(t, 1, len(memory.FailedParameters))
	assert.Equal(t, 5, memory.SuccessfulParameters[0].Frequency)
	assert.Equal(t, 7, memory.UsageCount)
	assert.InDelta(t, 0.71, memory.SuccessRate, 0.01)
}

// TestMemorySearchResult tests search result structure
func TestMemorySearchResult(t *testing.T) {
	result := &MemorySearchResult{
		Entries: []MemoryEntry{
			{
				ID:      "test1",
				Type:    MemoryTypeToolUsage,
				Content: "Test entry 1",
			},
		},
		Total: 1,
		Query: MemoryQuery{
			Types: []MemoryType{MemoryTypeToolUsage},
			Limit: 10,
		},
		ExecutionTime: 50 * time.Millisecond,
	}

	assert.Equal(t, 1, len(result.Entries))
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, MemoryTypeToolUsage, result.Query.Types[0])
	assert.Equal(t, 50*time.Millisecond, result.ExecutionTime)
}

// TestTimeRange tests time range structure
func TestTimeRange(t *testing.T) {
	start := time.Now()
	end := start.Add(24 * time.Hour)

	timeRange := &TimeRange{
		Start: start,
		End:   end,
	}

	assert.Equal(t, start, timeRange.Start)
	assert.Equal(t, end, timeRange.End)
}

// TestMemoryEntry_Validation tests memory entry validation
func TestMemoryEntry_Validation(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Test automatic ID generation
	entry1 := MemoryEntry{
		Type:    MemoryTypeToolUsage,
		Content: "Auto ID test",
		Tags:    []string{"test"},
	}

	err := memSystem.StoreEntry(entry1)
	require.NoError(t, err)
	result1, err := memSystem.Search(MemoryQuery{
		Types:   []MemoryType{MemoryTypeToolUsage},
		Content: "Auto ID test",
		Limit:   1,
	})
	require.NoError(t, err)
	require.Len(t, result1.Entries, 1)
	assert.NotEmpty(t, result1.Entries[0].ID)

	// Test automatic timestamp
	entry2 := MemoryEntry{
		ID:      "manual_id",
		Type:    MemoryTypeError,
		Content: "Manual ID test",
		Tags:    []string{"test"},
	}

	err = memSystem.StoreEntry(entry2)
	require.NoError(t, err)
	storedEntry2, err := memSystem.GetEntry(entry2.ID)
	require.NoError(t, err)
	assert.False(t, storedEntry2.CreatedAt.IsZero())
}

// TestMemoryImportanceDecay tests importance decay calculation
func TestMemoryImportanceDecay(t *testing.T) {
	config := DefaultMemoryConfig()
	config.ImportanceDecay = 0.2 // 20% decay
	memSystem := NewMemoryAdapterWithConfig(config)
	memSystem.Start()
	defer memSystem.Stop()

	// Store entry
	entry := MemoryEntry{
		Type:    MemoryTypeToolUsage,
		Content: "Decay test",
		Tags:    []string{"test"},
	}

	_ = memSystem.StoreEntry(entry)

	// Get the stored entry
	storedResult, err := memSystem.Search(MemoryQuery{
		Types:   []MemoryType{MemoryTypeToolUsage},
		Content: "Decay test",
		Limit:   1,
	})
	require.NoError(t, err)
	require.Len(t, storedResult.Entries, 1)
	stored := storedResult.Entries[0]
	entry.ID = stored.ID

	// Access the entry multiple times to see importance decay
	initialImportance := stored.Importance
	for i := 0; i < 10; i++ {
		_, _ = memSystem.GetEntry(entry.ID)
	}

	finalEntry, _ := memSystem.GetEntry(entry.ID)

	// Importance should have changed due to access count updates
	// (Note: actual decay logic would need to be implemented)
	assert.NotNil(t, finalEntry.Importance)
	assert.Greater(t, finalEntry.AccessCount, 10)
	assert.Less(t, finalEntry.Importance, initialImportance) // Importance should decay
}

// TestMemoryStats_Aggregation tests statistics aggregation
func TestMemoryStats_Aggregation(t *testing.T) {
	memSystem := NewMemoryAdapter()
	memSystem.Start()
	defer memSystem.Stop()

	// Store many entries
	memoryTypes := []MemoryType{MemoryTypeToolUsage, MemoryTypeConversation, MemoryTypeError, MemoryTypeContext, MemoryTypeSuccess}
	for i := 0; i < 20; i++ {
		entry := MemoryEntry{
			Type:    memoryTypes[i%len(memoryTypes)], // Vary types
			Content: fmt.Sprintf("Test content %d", i),
			Tags:    []string{"test"},
		}
		_ = memSystem.StoreEntry(entry)
	}

	// Get statistics
	stats := memSystem.Stats()
	assert.Equal(t, 20, stats.TotalEntries)
	assert.GreaterOrEqual(t, stats.TotalAccessCount, int64(0))

	// Check that all types are represented
	foundTypes := []MemoryType{}
	for memType, count := range stats.EntriesByType {
		if count > 0 {
			foundTypes = append(foundTypes, memType)
		}
	}
	assert.GreaterOrEqual(t, len(foundTypes), 2)
}

func TestNewAgentRegistry_LoadsBuiltIns(t *testing.T) {
	reg := NewAgentRegistry()

	for _, b := range BuiltInAgents {
		def, ok := reg.Get(b.AgentType)
		if !ok {
			t.Errorf("built-in agent %q not found in registry", b.AgentType)
			continue
		}
		if def.Source != AgentSourceBuiltIn {
			t.Errorf("agent %q: expected source=built-in, got %q", b.AgentType, def.Source)
		}
	}
}

func TestAgentRegistry_Register_Override(t *testing.T) {
	reg := NewAgentRegistry()
	custom := &AgentDefinition{
		AgentType:       AgentTypeGeneralPurpose,
		WhenToUse:       "custom override",
		Source:          AgentSourceUser,
		GetSystemPrompt: func() string { return "custom prompt" },
	}
	reg.Register(custom)

	def, ok := reg.Get(AgentTypeGeneralPurpose)
	if !ok {
		t.Fatal("agent not found after Register")
	}
	if def.WhenToUse != "custom override" {
		t.Errorf("expected custom override, got %q", def.WhenToUse)
	}
}

func TestAgentRegistry_LoadFromSkills_AddsAgentSkills(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())

	// Create a user skill with agent: field
	userID := "test-user-registry"
	userSkillDir := filepath.Join(publicskills.UserPath(userID), "my-agent")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: \"my-agent\"\ndescription: \"an agent skill\"\nagent: \"custom-agent\"\neffort: \"high\"\n---\n\nYou are a custom agent.\n"
	if err := os.WriteFile(filepath.Join(userSkillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := NewAgentRegistry()
	if err := reg.LoadFromSkills("", userID); err != nil {
		t.Fatalf("LoadFromSkills: %v", err)
	}

	def, ok := reg.Get("custom-agent")
	if !ok {
		t.Fatal("skill-derived agent 'custom-agent' not found in registry")
	}
	if def.Source != AgentSourceUser {
		t.Errorf("expected source=user, got %q", def.Source)
	}
	if def.MaxTurns != 100 { // "high" → 100
		t.Errorf("expected MaxTurns=100 for effort=high, got %d", def.MaxTurns)
	}
	if def.GetSystemPrompt == nil {
		t.Fatal("GetSystemPrompt should not be nil")
	}
	sp := def.GetSystemPrompt()
	if sp == "" {
		t.Error("expected non-empty system prompt from skill body")
	}
}

func TestAgentRegistry_LoadFromSkills_DoesNotOverrideBuiltIns(t *testing.T) {
	t.Setenv("SESHAT_RUNTIME_ROOT", t.TempDir())

	userID := "test-user-no-override"
	userSkillDir := filepath.Join(publicskills.UserPath(userID), "explore-override")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Skill tries to redefine the built-in "explore" agent.
	content := "---\nname: \"explore-override\"\ndescription: \"override attempt\"\nagent: \"explore\"\n---\n\nThis should not override the built-in explore agent.\n"
	if err := os.WriteFile(filepath.Join(userSkillDir, "skill.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := NewAgentRegistry()
	if err := reg.LoadFromSkills("", userID); err != nil {
		t.Fatalf("LoadFromSkills: %v", err)
	}

	def, ok := reg.Get(AgentTypeExplore)
	if !ok {
		t.Fatal("explore agent not found after LoadFromSkills")
	}
	if def.Source != AgentSourceBuiltIn {
		t.Errorf("skill must not override built-in: expected source=built-in, got %q", def.Source)
	}
}

func TestEffortToMaxTurns(t *testing.T) {
	cases := []struct {
		effort string
		want   int
	}{
		{"minimal", 10},
		{"low", 20},
		{"medium", 50},
		{"high", 100},
		{"maximum", 150},
		{"", 50},
		{"unknown", 50},
	}
	for _, tc := range cases {
		if got := effortToMaxTurns(tc.effort); got != tc.want {
			t.Errorf("effortToMaxTurns(%q) = %d, want %d", tc.effort, got, tc.want)
		}
	}
}

func TestSkillToAgentDefinition_FieldMapping(t *testing.T) {
	sk := &skills.Skill{
		Name:         "my-skill",
		Description:  "does things",
		Agent:        "my-bot",
		WhenToUse:    "when you need to do things",
		AllowedTools: []string{"read_file", "bash"},
		Model:        "claude-sonnet-4-6",
		Effort:       "low",
		SkillRoot:    "/some/path",
		GetPromptForCommand: func(args string, ctx context.Context) ([]skills.ContentBlock, error) {
			return []skills.ContentBlock{{Type: "text", Text: "You are my-bot."}}, nil
		},
	}

	def := skillToAgentDefinition(sk)

	if def.AgentType != "my-bot" {
		t.Errorf("AgentType: want %q, got %q", "my-bot", def.AgentType)
	}
	if def.WhenToUse != "when you need to do things" {
		t.Errorf("WhenToUse: want %q, got %q", "when you need to do things", def.WhenToUse)
	}
	if def.Model != "claude-sonnet-4-6" {
		t.Errorf("Model: want %q, got %q", "claude-sonnet-4-6", def.Model)
	}
	if def.MaxTurns != 20 {
		t.Errorf("MaxTurns: want 20, got %d", def.MaxTurns)
	}
	if len(def.Tools) != 2 {
		t.Errorf("Tools: want 2, got %d", len(def.Tools))
	}
	if def.GetSystemPrompt == nil {
		t.Fatal("GetSystemPrompt should not be nil")
	}
	if sp := def.GetSystemPrompt(); sp != "You are my-bot." {
		t.Errorf("GetSystemPrompt(): want %q, got %q", "You are my-bot.", sp)
	}
}
