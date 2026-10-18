package memory

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// The session store archives what a compaction drops only when the compacted conversation carries compaction
// metadata (see state.droppedByCompaction). An emergency trim has no summary, so its marker must carry it.
func TestAnEmergencyTrimIsRecognisedAsACompaction(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	result, ok := e.EmergencyTrim("", bigConversation(200, 1_000), emergencyModel, nil)
	if !ok {
		t.Fatal("expected a trim")
	}
	if types.CompactionMetadataFromMessages(result.Messages) == nil {
		t.Error("the trimmed conversation must be recognised as compacted, or what it dropped is not archived")
	}
	if err := types.ValidateCompactionBoundary(result.Messages); err != nil {
		t.Errorf("the trimmed conversation must pass the boundary check when restored: %v", err)
	}
}
