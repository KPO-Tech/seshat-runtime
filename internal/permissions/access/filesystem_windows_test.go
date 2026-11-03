package access

import "testing"

// This file is only compiled on windows (filename convention) — it verifies
// the Windows-specific FilesystemPolicy prefixes and case-insensitive
// matching added alongside the Docker sandbox work never actually had a
// passing regression test: every existing sandbox_test.go path test uses
// hardcoded Unix paths and fails outright on Windows (confirmed pre-existing
// and unrelated), so nothing exercised this code for real before.

func TestHasPathPrefixWindowsCaseInsensitive(t *testing.T) {
	if !hasPathPrefix(`C:\Windows\System32\evil.dll`, `C:\Windows`) {
		t.Fatal(`expected C:\Windows\System32\evil.dll to match prefix C:\Windows`)
	}
	if !hasPathPrefix(`c:\windows\system32\evil.dll`, `C:\Windows`) {
		t.Fatal("expected case-insensitive match on Windows (NTFS is case-preserving, not case-sensitive)")
	}
	if hasPathPrefix(`C:\WindowsFake\evil.dll`, `C:\Windows`) {
		t.Fatal(`expected C:\WindowsFake to NOT match prefix C:\Windows — must respect the path segment boundary, not just a naive string prefix`)
	}
}

func TestFilesystemPolicyDeniesWriteToWindowsSystemDirs(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	ctx := Context{}
	for _, path := range []string{
		`C:\Windows\System32\evil.dll`,
		`C:\Program Files\SomeApp\evil.exe`,
		`C:\Program Files (x86)\SomeApp\evil.exe`,
		// Case-insensitive variant, proving the property end-to-end through
		// EvaluatePath, not just the hasPathPrefix unit above.
		`c:\windows\system32\evil.dll`,
	} {
		decision, err := policy.EvaluatePath(ctx, path, AccessWrite)
		if err != nil {
			t.Fatalf("EvaluatePath(%q): %v", path, err)
		}
		if decision.Decision != DecisionDeny {
			t.Fatalf("expected write to %q to be denied, got %v", path, decision.Decision)
		}
	}
}

func TestFilesystemPolicyAllowsWriteOutsideWindowsProtectedDirs(t *testing.T) {
	policy := NewDefaultFilesystemPolicy()
	ctx := Context{}
	decision, err := policy.EvaluatePath(ctx, `C:\Users\someone\project\file.txt`, AccessWrite)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if decision.Decision != DecisionAllow {
		t.Fatalf("expected write outside protected dirs to be allowed, got %v (%s)", decision.Decision, decision.Reason)
	}
}
