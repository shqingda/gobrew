package model

import "testing"

func TestValidPathComponentRejectsTraversal(t *testing.T) {
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "../x"} {
		if ValidPathComponent(s) {
			t.Errorf("%q accepted", s)
		}
	}
	for _, s := range []string{"jq", "1.8.2_1", "..hidden", "python@3.13"} {
		if !ValidPathComponent(s) {
			t.Errorf("%q rejected", s)
		}
	}
}
