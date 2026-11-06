package agent

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func hostTypes(r *AgentRegistry) []string {
	var out []string
	for _, d := range r.HostDefinitions() {
		out = append(out, d.AgentType)
	}
	return out
}

func TestValidateHostAgentType(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"simple", "reviewer", true},
		{"dashes and digits", "code-review-2", true},
		{"single char", "a", true},
		{"digits only", "42", true},
		{"max length", "a" + repeat("b", 62) + "c", true},
		{"empty", "", false},
		{"too long", repeat("a", 65), false},
		{"uppercase", "Reviewer", false},
		{"underscore", "code_review", false},
		{"space", "code review", false},
		{"leading dash", "-reviewer", false},
		{"trailing dash", "reviewer-", false},
		{"double dash", "code--review", false},
		{"slash", "a/b", false},
		{"dot", "a.b", false},
		{"colon (skill-fork namespace)", "skill-fork:x", false},
		{"unicode", "révision", false},
		{"newline", "a\nb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateHostAgentType(tt.input); (err == nil) != tt.ok {
				t.Errorf("ValidateHostAgentType(%q) = %v, want ok=%v", tt.input, err, tt.ok)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestReplaceHostDefinitions_RegisterReplaceRemove(t *testing.T) {
	r := NewAgentRegistry()
	builtIns := len(r.All())

	if err := r.ReplaceHostDefinitions([]*AgentDefinition{{AgentType: "alpha"}, {AgentType: "beta"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := hostTypes(r), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after register: %v, want %v", got, want)
	}
	if _, ok := r.Get("alpha"); !ok {
		t.Fatal("alpha not resolvable")
	}

	// Replace: the whole set, so beta goes and gamma arrives.
	if err := r.ReplaceHostDefinitions([]*AgentDefinition{{AgentType: "alpha", MaxTurns: 7}, {AgentType: "gamma"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := hostTypes(r), []string{"alpha", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after replace: %v, want %v", got, want)
	}
	if def, _ := r.Get("alpha"); def.MaxTurns != 7 {
		t.Errorf("alpha was not replaced: %+v", def)
	}
	if _, ok := r.Get("beta"); ok {
		t.Error("beta survived a replace that left it out")
	}

	// Remove: an empty set clears every host definition and nothing else.
	if err := r.ReplaceHostDefinitions(nil); err != nil {
		t.Fatal(err)
	}
	if len(hostTypes(r)) != 0 || len(r.All()) != builtIns {
		t.Fatalf("after clear: host=%v total=%d (built-ins %d)", hostTypes(r), len(r.All()), builtIns)
	}
}

func TestReplaceHostDefinitions_ShadowingRefused(t *testing.T) {
	r := NewAgentRegistry()
	// A skill-defined agent already in the registry.
	r.Register(&AgentDefinition{AgentType: "from-skill", Source: AgentSourceUser, Tools: []string{"read_file"}})

	err := r.ReplaceHostDefinitions([]*AgentDefinition{
		{AgentType: "explore", Tools: []string{"*"}},
		{AgentType: "general-purpose"},
		{AgentType: "from-skill"},
		{AgentType: "fine"},
	})
	if !errors.Is(err, ErrShadowsExistingAgent) {
		t.Fatalf("err = %v, want ErrShadowsExistingAgent", err)
	}
	var herr *HostDefinitionError
	if !errors.As(err, &herr) {
		t.Fatalf("err does not carry a *HostDefinitionError: %v", err)
	}
	// The built-in and the skill agent are untouched.
	if def, _ := r.Get("explore"); def.Source != AgentSourceBuiltIn {
		t.Errorf("built-in explore was replaced: %+v", def)
	}
	if def, _ := r.Get("from-skill"); def.Source != AgentSourceUser || len(def.Tools) != 1 {
		t.Errorf("skill agent was replaced: %+v", def)
	}
	// The bad ones did not break the good one.
	if got, want := hostTypes(r), []string{"fine"}; !reflect.DeepEqual(got, want) {
		t.Errorf("host set = %v, want %v", got, want)
	}
}

func TestReplaceHostDefinitions_BadDefinitionsDoNotBreakOthers(t *testing.T) {
	r := NewAgentRegistry()
	err := r.ReplaceHostDefinitions([]*AgentDefinition{
		{AgentType: "good-one"},
		nil,
		{AgentType: "Bad Name"},
		{AgentType: ""},
		{AgentType: "dup"},
		{AgentType: "dup", MaxTurns: 99},
		{AgentType: "good-two"},
	})
	if err == nil {
		t.Fatal("expected an error for the rejected definitions")
	}
	if got, want := hostTypes(r), []string{"dup", "good-one", "good-two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("host set = %v, want %v", got, want)
	}
	if def, _ := r.Get("dup"); def.MaxTurns != 0 {
		t.Errorf("the first of two duplicates must win, got %+v", def)
	}
}

func TestReplaceHostDefinitions_StoresAPrivateCopyMarkedHost(t *testing.T) {
	r := NewAgentRegistry()
	tools := []string{"read_file"}
	deny := []string{"bash"}
	in := &AgentDefinition{AgentType: "mine", Source: AgentSourceBuiltIn, Tools: tools, DisallowedTools: deny}
	if err := r.ReplaceHostDefinitions([]*AgentDefinition{in}); err != nil {
		t.Fatal(err)
	}

	// The host mutating what it passed must not change the policy in force.
	tools[0] = "*"
	deny[0] = "nothing"
	in.Source = AgentSourceBuiltIn

	def, _ := r.Get("mine")
	if def.Source != AgentSourceHost {
		t.Errorf("Source = %q; a host cannot pose as built-in", def.Source)
	}
	if def.Tools[0] != "read_file" || def.DisallowedTools[0] != "bash" {
		t.Errorf("registry aliases the host's slices: %+v", def)
	}
	// Copies returned to callers are private too.
	r.HostDefinitions()[0].DisallowedTools[0] = "changed"
	if def, _ := r.Get("mine"); def.DisallowedTools[0] != "bash" {
		t.Error("HostDefinitions leaks the live definition")
	}
}

// Replace is atomic: a reader never sees a set that is half old, half new.
// Run with -race.
func TestReplaceHostDefinitions_ConcurrentUse(t *testing.T) {
	r := NewAgentRegistry()
	setA := []*AgentDefinition{{AgentType: "a-one"}, {AgentType: "a-two"}}
	setB := []*AgentDefinition{{AgentType: "b-one"}, {AgentType: "b-two"}}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := hostTypes(r)
				switch fmt.Sprint(got) {
				case "[]", "[a-one a-two]", "[b-one b-two]":
				default:
					t.Errorf("torn read: %v", got)
					return
				}
				_ = r.All()
				_, _ = r.Get("a-one")
			}
		}()
	}
	var writers sync.WaitGroup
	for w := 0; w < 3; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 200; i++ {
				switch (i + w) % 3 {
				case 0:
					_ = r.ReplaceHostDefinitions(setA)
				case 1:
					_ = r.ReplaceHostDefinitions(setB)
				default:
					_ = r.ReplaceHostDefinitions(nil)
				}
			}
		}(w)
	}
	writers.Wait()
	close(stop)
	wg.Wait()
}
