package bash

import "testing"

func TestValidateCommandRefusesHiddenCharacters(t *testing.T) {
	v := NewSecurityValidator()
	traps := map[string]string{
		"direction override": "echo safe ‮ txt.sh",
		"isolate":            "echo ⁦safe",
		"tag character":      "echo \U000E0041",
		"escape":             "echo \x1b[2J",
		"nul":                "echo a\x00b",
	}
	for name, cmd := range traps {
		viol := v.ValidateCommand(cmd)
		if viol == nil || viol.Violation != "hidden-character" {
			t.Errorf("%s: want a hidden-character violation, got %+v", name, viol)
		}
	}
}

func TestValidateCommandAllowsOrdinaryUnicode(t *testing.T) {
	v := NewSecurityValidator()
	for _, cmd := range []string{
		"echo héllo 日本語",
		"git commit -m \"fix 👨‍👩‍👧 emoji ❤️\"",
		"printf 'a\\tb'\nls",
		"echo a\r\necho b",
	} {
		if viol := v.ValidateCommand(cmd); viol != nil {
			t.Errorf("%q must pass, got %+v", cmd, viol)
		}
	}
}
