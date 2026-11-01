package cli

import (
	"testing"
)

func TestColumnsMatchHomebrewLayout(t *testing.T) {
	items := []string{"aa", "bbbb", "c", "dd", "e"}
	widths := []int{2, 4, 1, 2, 1}
	// Homebrew: 3 columns fit, 2 rows; column width (20+2)/3-2 = 5, then a 2-space gap.
	want := "aa     c      e\nbbbb   dd\n"
	if got := columns(items, widths, 20); got != want {
		t.Fatalf("columns = %q, want %q", got, want)
	}
	if got := columns(items, widths, 5); got != "aa\nbbbb\nc\ndd\ne\n" {
		t.Fatalf("narrow columns = %q", got)
	}
}
