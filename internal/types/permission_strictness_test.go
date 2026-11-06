package types

import "testing"

func TestPermissionModeStrictness_Order(t *testing.T) {
	order := []PermissionMode{
		PermissionModeNever,
		PermissionModeGranular,
		PermissionModeOnRequest,
		PermissionModeAcceptEdits,
		PermissionModeAuto,
		PermissionModeBypass,
	}
	for i, m := range order {
		rank, ok := PermissionModeStrictness(m)
		if !ok || rank != i {
			t.Errorf("PermissionModeStrictness(%q) = %d,%v want %d,true", m, rank, ok, i)
		}
	}
	for _, m := range []PermissionMode{"", "plan", "Bypass", "yolo"} {
		if _, ok := PermissionModeStrictness(m); ok {
			t.Errorf("PermissionModeStrictness(%q) must be unknown", m)
		}
	}
}

func TestStricterPermissionMode(t *testing.T) {
	tests := []struct {
		name string
		a, b PermissionMode
		want PermissionMode
	}{
		{"never beats everything", PermissionModeBypass, PermissionModeNever, PermissionModeNever},
		{"granular below onRequest", PermissionModeOnRequest, PermissionModeGranular, PermissionModeGranular},
		{"acceptEdits below auto", PermissionModeAuto, PermissionModeAcceptEdits, PermissionModeAcceptEdits},
		{"auto below bypass", PermissionModeBypass, PermissionModeAuto, PermissionModeAuto},
		{"equal", PermissionModeAuto, PermissionModeAuto, PermissionModeAuto},
		{"unknown vs bypass", "yolo", PermissionModeBypass, PermissionModeOnRequest},
		{"unknown vs never", "yolo", PermissionModeNever, PermissionModeNever},
		{"unknown vs granular", PermissionModeGranular, "yolo", PermissionModeGranular},
		{"unknown vs auto", PermissionModeAuto, "", PermissionModeOnRequest},
		{"both unknown", "a", "b", PermissionModeOnRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StricterPermissionMode(tt.a, tt.b); got != tt.want {
				t.Errorf("StricterPermissionMode(%q,%q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
			if got := StricterPermissionMode(tt.b, tt.a); got != tt.want {
				t.Errorf("StricterPermissionMode(%q,%q) = %q, want %q (symmetry)", tt.b, tt.a, got, tt.want)
			}
		})
	}
}
