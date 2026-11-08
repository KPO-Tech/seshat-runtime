package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// An empty request must leave the server's configuration alone: a client that
// only wants a conversation should not have to know the defaults.
func TestApplySessionOptionsKeepsDefaults(t *testing.T) {
	cfg := &sdk.ClientConfig{MaxTurns: 100, MaxIterations: 42, AutoCompact: true}
	if err := applySessionOptions(cfg, &pb.CreateSessionRequest{}); err != nil {
		t.Fatalf("applySessionOptions: %v", err)
	}
	if cfg.MaxTurns != 100 || cfg.MaxIterations != 42 {
		t.Fatalf("defaults changed: turns=%d iterations=%d", cfg.MaxTurns, cfg.MaxIterations)
	}
	if !cfg.AutoCompact {
		t.Fatal("auto_compact was turned off by an unset field")
	}
	if cfg.EnableToolSearch {
		t.Fatal("enable_tool_search was turned on by an unset field")
	}
}

func TestApplySessionOptionsAppliesEveryKnob(t *testing.T) {
	cfg := &sdk.ClientConfig{AutoCompact: true}
	req := &pb.CreateSessionRequest{
		MaxIterations:         7,
		MaxTurns:              12,
		TurnTokenBudget:       50000,
		MaxConsecutiveDenials: 4,
		AutoCompact:           proto.Bool(false),
		EnableToolSearch:      proto.Bool(true),
	}
	if err := applySessionOptions(cfg, req); err != nil {
		t.Fatalf("applySessionOptions: %v", err)
	}

	if cfg.MaxIterations != 7 {
		t.Fatalf("max_iterations = %d, want 7", cfg.MaxIterations)
	}
	if cfg.MaxTurns != 12 {
		t.Fatalf("max_turns = %d, want 12", cfg.MaxTurns)
	}
	if cfg.TurnTokenBudget != 50000 {
		t.Fatalf("turn_token_budget = %d, want 50000", cfg.TurnTokenBudget)
	}
	if cfg.MaxConsecutiveDenials != 4 {
		t.Fatalf("max_consecutive_denials = %d, want 4", cfg.MaxConsecutiveDenials)
	}
	if cfg.AutoCompact {
		t.Fatal("auto_compact=false was ignored")
	}
	if !cfg.EnableToolSearch {
		t.Fatal("enable_tool_search=true was ignored")
	}
}

// An explicit `false` must reach the server, which is the whole reason the two
// booleans are `optional`: a plain bool cannot tell "off" from "unset".
func TestApplySessionOptionsCanTurnBooleansOff(t *testing.T) {
	cfg := &sdk.ClientConfig{EnableToolSearch: true}
	if err := applySessionOptions(cfg, &pb.CreateSessionRequest{EnableToolSearch: proto.Bool(false)}); err != nil {
		t.Fatalf("applySessionOptions: %v", err)
	}
	if cfg.EnableToolSearch {
		t.Fatal("enable_tool_search=false was ignored")
	}
}

func TestApplySessionOptionsRejectsNegatives(t *testing.T) {
	cases := map[string]*pb.CreateSessionRequest{
		"max_iterations":          {MaxIterations: -1},
		"max_turns":               {MaxTurns: -5},
		"turn_token_budget":       {TurnTokenBudget: -100},
		"max_consecutive_denials": {MaxConsecutiveDenials: -2},
	}
	for name, req := range cases {
		err := applySessionOptions(&sdk.ClientConfig{}, req)
		if err == nil {
			t.Fatalf("%s: negative value was accepted", name)
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: code = %v, want InvalidArgument", name, status.Code(err))
		}
	}
}
