package parser

import (
	"reflect"
	"testing"
)

func TestBoxesOnOneLineAreReadFromTheLeftWhateverTheirTopEdge(t *testing.T) {
	// "Pine" sits one pixel lower than "Harbour Water Utilities LLC" to its right: they are one line, and Pine comes first.
	boxes := []spot{
		{x0: 215, y0: 100, x1: 700, y1: 124}, // Harbour Water Utilities LLC
		{x0: 105, y0: 101, x1: 140, y1: 125}, // Pine
	}
	if got, want := readingOrder(boxes), []int{1, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestLinesAreReadFromTheTopEvenWhenTheirBoxesAreCloseInHeight(t *testing.T) {
	// Two lines 26 pixels apart, with boxes 24 high: not one line.
	boxes := []spot{
		{x0: 10, y0: 126, x1: 90, y1: 150},   // second line, left
		{x0: 100, y0: 100, x1: 190, y1: 124}, // first line, right
		{x0: 10, y0: 100, x1: 90, y1: 124},   // first line, left
	}
	if got, want := readingOrder(boxes), []int{2, 1, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestNoBoxesGiveNoOrder(t *testing.T) {
	if got := readingOrder(nil); len(got) != 0 {
		t.Errorf("order = %v, want none", got)
	}
}
