package registry

import "testing"

func TestToolPolicyPermits(t *testing.T) {
	tests := []struct {
		name   string
		policy ToolPolicy
		names  []string
		want   bool
	}{
		{"zero policy permits", ToolPolicy{}, []string{"bash"}, true},
		{"tools only: listed", ToolPolicy{}.Narrow([]string{"read_file", "grep"}, nil), []string{"grep"}, true},
		{"tools only: not listed", ToolPolicy{}.Narrow([]string{"read_file", "grep"}, nil), []string{"bash"}, false},
		{"non-nil empty allow means nothing", ToolPolicy{}.Narrow([]string{}, nil), []string{"bash"}, false},
		{"tools star means all", ToolPolicy{}.Narrow([]string{"*"}, nil), []string{"bash"}, true},
		{"disallowed only subtracts from all", ToolPolicy{}.Narrow([]string{"*"}, []string{"bash"}), []string{"bash"}, false},
		{"disallowed only leaves the rest", ToolPolicy{}.Narrow([]string{"*"}, []string{"bash"}), []string{"grep"}, true},
		{"nil allow with deny", ToolPolicy{}.Narrow(nil, []string{"bash"}), []string{"grep"}, true},
		{"both overlapping: deny wins", ToolPolicy{}.Narrow([]string{"bash", "grep"}, []string{"bash"}), []string{"bash"}, false},
		{"both overlapping: the rest of allow stays", ToolPolicy{}.Narrow([]string{"bash", "grep"}, []string{"bash"}), []string{"grep"}, true},
		{"deny prefix wildcard", ToolPolicy{}.Narrow([]string{"*"}, []string{"mcp__*"}), []string{"mcp__db__query"}, false},
		{"deny suffix wildcard", ToolPolicy{}.Narrow([]string{"*"}, []string{"*_file"}), []string{"write_file"}, false},
		{"deny wildcard does not over-match", ToolPolicy{}.Narrow([]string{"*"}, []string{"mcp__*"}), []string{"web_fetch"}, true},
		{"deny wildcard beats allow wildcard", ToolPolicy{}.Narrow([]string{"web_*"}, []string{"web_fetch"}), []string{"web_fetch"}, false},
		{"allow wildcard", ToolPolicy{}.Narrow([]string{"web_*"}, nil), []string{"web_search"}, true},
		{"deny matches an alias of the tool", ToolPolicy{}.Narrow([]string{"*"}, []string{"write_file"}), []string{"Write", "write_file"}, false},
		{"deny matches the key when the canonical differs", ToolPolicy{}.Narrow([]string{"*"}, []string{"Write"}), []string{"Write", "write_file"}, false},
		{"two allow layers must both match", ToolPolicy{}.Narrow([]string{"a", "b"}, nil).Narrow([]string{"b", "c"}, nil), []string{"a"}, false},
		{"two allow layers: the common tool", ToolPolicy{}.Narrow([]string{"a", "b"}, nil).Narrow([]string{"b", "c"}, nil), []string{"b"}, true},
		{"child cannot regain a tool the parent denied", ToolPolicy{}.Narrow(nil, []string{"bash"}).Narrow([]string{"bash"}, nil), []string{"bash"}, false},
		{"child cannot regain through star", ToolPolicy{}.Narrow([]string{"grep"}, nil).Narrow([]string{"*"}, nil), []string{"bash"}, false},
		{"empty-string deny is ignored", ToolPolicy{}.Narrow(nil, []string{""}), []string{"bash"}, true},
		{"merge keeps both sides", ToolPolicy{}.Narrow(nil, []string{"a"}).Merge(ToolPolicy{}.Narrow([]string{"b", "c"}, []string{"c"})), []string{"b"}, true},
		{"merge: deny from the left", ToolPolicy{}.Narrow(nil, []string{"a"}).Merge(ToolPolicy{}.Narrow([]string{"a", "b"}, nil)), []string{"a"}, false},
		{"merge: deny from the right", ToolPolicy{}.Narrow([]string{"a", "b"}, nil).Merge(ToolPolicy{}.Narrow(nil, []string{"b"})), []string{"b"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.Permits(tt.names...); got != tt.want {
				t.Errorf("Permits(%v) = %v, want %v", tt.names, got, tt.want)
			}
		})
	}
}

func TestToolPolicyNarrowDoesNotMutateReceiver(t *testing.T) {
	parent := ToolPolicy{}.Narrow([]string{"a"}, []string{"x"})
	_ = parent.Narrow([]string{"b"}, []string{"y"})
	if len(parent.Allow) != 1 || len(parent.Deny) != 1 {
		t.Fatalf("receiver mutated: %+v", parent)
	}
}

func TestToolPolicyIsZero(t *testing.T) {
	if !(ToolPolicy{}).IsZero() || !(ToolPolicy{}.Narrow([]string{"*"}, nil)).IsZero() {
		t.Error("star allow should restrict nothing")
	}
	if (ToolPolicy{}.Narrow(nil, []string{"x"})).IsZero() || (ToolPolicy{}.Narrow([]string{}, nil)).IsZero() || (ToolPolicy{}.Narrow([]string{"a"}, nil)).IsZero() {
		t.Error("a deny, an empty non-nil allow and a narrow allow all restrict")
	}
}
