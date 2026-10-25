package parser

import (
	"math"
	"sort"
)

// sameLineShare is how far, as a share of the height of a box, the centre of another box may be from its centre and still be on the
// same line of text. Two lines are at least one height apart, so half a height keeps the lines apart.
const sameLineShare = 0.5

// spot is the rectangle of a detected piece of text, in pixels.
type spot struct{ x0, y0, x1, y1 float64 }

func (s spot) centreY() float64 { return (s.y0 + s.y1) / 2 }
func (s spot) height() float64  { return math.Max(s.y1-s.y0, 1) }

// readingOrder gives the order in which to read the boxes of a page: line by line from the top, and from the left within a line. The
// boxes are first put into lines by where they sit (the centre of one within half a height of the centre of the first of its line),
// and only then ordered by x. Ordering by the top edge as it is would put a box one pixel higher than its neighbour on the left
// before it ("Harbour Water Utilities LLC" read before "Pine"), though they are one line.
func readingOrder(boxes []spot) []int {
	byY := make([]int, len(boxes))
	for i := range byY {
		byY[i] = i
	}
	sort.SliceStable(byY, func(a, b int) bool { return boxes[byY[a]].centreY() < boxes[byY[b]].centreY() })

	var lines [][]int
	for _, i := range byY {
		if n := len(lines); n > 0 {
			first := boxes[lines[n-1][0]]
			if math.Abs(boxes[i].centreY()-first.centreY()) <= sameLineShare*math.Max(first.height(), boxes[i].height()) {
				lines[n-1] = append(lines[n-1], i)
				continue
			}
		}
		lines = append(lines, []int{i})
	}
	order := make([]int, 0, len(boxes))
	for _, line := range lines {
		sort.SliceStable(line, func(a, b int) bool { return boxes[line[a]].x0 < boxes[line[b]].x0 })
		order = append(order, line...)
	}
	return order
}
