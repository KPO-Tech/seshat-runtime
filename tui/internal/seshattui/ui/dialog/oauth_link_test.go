package dialog

import "testing"

func TestOpenWebLinkRefusesOtherSchemes(t *testing.T) {
	for _, raw := range []string{"file:///C:/Windows/System32/calc.exe", "ms-msdt:/id", "javascript:alert(1)", "", "https://", "//evil.example"} {
		if err := openWebLink(raw); err == nil {
			t.Errorf("%q must be refused", raw)
		}
	}
}
