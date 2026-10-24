package pdftext

import (
	"math"
	"sort"

	"github.com/KPO-Tech/seshat-runtime/internal/pdfread"
)

// A table drawn with ruling lines says where its cells are better than any amount of guessing from the text, so
// the lines are what table detection starts from. The library reports the page's text and its "re" rectangles but
// not the lines drawn with m/l and stroked, so the content stream is read here: the path operators, with the
// transformation matrix and the line width they are drawn with.

// hline is a horizontal ruling at y from x0 to x1, vline a vertical one at x from y0 to y1, in page units (y up).
type hline struct{ y, x0, x1 float64 }
type vline struct{ x, y0, y1 float64 }

type rulings struct {
	h []hline
	v []vline
}

const (
	axisTolerance = 0.6 // a segment this close to level or plumb is a horizontal or vertical ruling
	thinFill      = 2.6 // a filled rectangle thinner than this, in points, is a ruling drawn as a bar
	minRuling     = 3.0 // shorter than this is a tick or a dot, not a ruling
)

// pageRulings reads the horizontal and vertical lines a page draws: stroked segments and rectangle edges, and
// filled rectangles thin enough to be a line (many producers draw rules that way).
func pageRulings(page pdf.Page) (r rulings, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			r, err = rulings{}, errorFromPanic(rec)
		}
	}()
	if page.V.IsNull() || page.V.Key("Contents").Kind() == pdf.Null {
		return rulings{}, nil
	}
	return collectRulings(page.V.Key("Contents")), nil
}

type affine [6]float64

var identity = affine{1, 0, 0, 1, 0, 0}

// mul returns a followed by b (the PDF convention: a is applied first).
func (a affine) mul(b affine) affine {
	return affine{
		a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3],
		a[4]*b[0] + a[5]*b[2] + b[4], a[4]*b[1] + a[5]*b[3] + b[5],
	}
}

func (a affine) apply(x, y float64) (float64, float64) {
	return a[0]*x + a[2]*y + a[4], a[1]*x + a[3]*y + a[5]
}

type pathSegment struct{ x0, y0, x1, y1 float64 }

type pathRect struct{ x, y, w, h float64 }

type graphicsState struct {
	ctm       affine
	lineWidth float64
}

func collectRulings(contents pdf.Value) rulings {
	var out rulings
	state := graphicsState{ctm: identity, lineWidth: 1}
	var stack []graphicsState
	var segments []pathSegment
	var rects []pathRect
	var cx, cy, sx, sy float64
	started := false

	clear := func() { segments, rects, started = nil, nil, false }
	closePath := func() {
		if started && (cx != sx || cy != sy) {
			segments = append(segments, pathSegment{cx, cy, sx, sy})
			cx, cy = sx, sy
		}
	}
	addLine := func(x0, y0, x1, y1 float64) {
		x0, y0 = state.ctm.apply(x0, y0)
		x1, y1 = state.ctm.apply(x1, y1)
		switch {
		case math.Abs(y1-y0) <= axisTolerance && math.Abs(x1-x0) >= minRuling:
			out.h = append(out.h, hline{(y0 + y1) / 2, math.Min(x0, x1), math.Max(x0, x1)})
		case math.Abs(x1-x0) <= axisTolerance && math.Abs(y1-y0) >= minRuling:
			out.v = append(out.v, vline{(x0 + x1) / 2, math.Min(y0, y1), math.Max(y0, y1)})
		}
	}
	stroke := func() {
		for _, s := range segments {
			addLine(s.x0, s.y0, s.x1, s.y1)
		}
	}
	fill := func() {
		for _, r := range rects {
			x0, y0 := state.ctm.apply(r.x, r.y)
			x1, y1 := state.ctm.apply(r.x+r.w, r.y+r.h)
			left, right := math.Min(x0, x1), math.Max(x0, x1)
			bottom, top := math.Min(y0, y1), math.Max(y0, y1)
			switch {
			case top-bottom <= thinFill && right-left >= minRuling && right-left > 2*(top-bottom):
				out.h = append(out.h, hline{(top + bottom) / 2, left, right})
			case right-left <= thinFill && top-bottom >= minRuling && top-bottom > 2*(right-left):
				out.v = append(out.v, vline{(left + right) / 2, bottom, top})
			}
		}
	}

	pdf.Interpret(contents, func(stk *pdf.Stack, op string) {
		n := stk.Len()
		args := make([]pdf.Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		switch op {
		case "q":
			stack = append(stack, state)
		case "Q":
			if len(stack) > 0 {
				state = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		case "cm":
			if len(args) == 6 {
				var m affine
				for i := range m {
					m[i] = args[i].Float64()
				}
				state.ctm = m.mul(state.ctm)
			}
		case "w":
			if len(args) == 1 {
				state.lineWidth = args[0].Float64()
			}
		case "m":
			if len(args) == 2 {
				cx, cy = args[0].Float64(), args[1].Float64()
				sx, sy = cx, cy
				started = true
			}
		case "l":
			if len(args) == 2 && started {
				x, y := args[0].Float64(), args[1].Float64()
				segments = append(segments, pathSegment{cx, cy, x, y})
				cx, cy = x, y
			}
		case "c", "v", "y":
			// A curve is not a ruling; it ends the straight run it interrupts.
			if len(args) >= 2 {
				cx, cy = args[len(args)-2].Float64(), args[len(args)-1].Float64()
			}
		case "re":
			if len(args) == 4 {
				x, y, w, h := args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64()
				rects = append(rects, pathRect{x, y, w, h})
				segments = append(segments,
					pathSegment{x, y, x + w, y}, pathSegment{x + w, y, x + w, y + h},
					pathSegment{x + w, y + h, x, y + h}, pathSegment{x, y + h, x, y})
				cx, cy, sx, sy, started = x, y, x, y, true
			}
		case "h":
			closePath()
		case "S":
			stroke()
			clear()
		case "s":
			closePath()
			stroke()
			clear()
		case "f", "F", "f*":
			fill()
			clear()
		case "B", "B*":
			fill()
			stroke()
			clear()
		case "b", "b*":
			closePath()
			fill()
			stroke()
			clear()
		case "n":
			clear()
		}
	})
	return out
}

// merged joins rulings that lie on the same line and touch or overlap, and snaps nearly equal coordinates
// together, so a rule drawn in several pieces, or twice, is one.
func (r rulings) merged() rulings {
	const snap, join = 1.5, 2.0
	var out rulings

	sort.Slice(r.h, func(i, j int) bool { return r.h[i].y < r.h[j].y })
	for i := 0; i < len(r.h); {
		j := i
		ys := 0.0
		for j < len(r.h) && r.h[j].y-r.h[i].y <= snap {
			ys += r.h[j].y
			j++
		}
		y := ys / float64(j-i)
		group := append([]hline(nil), r.h[i:j]...)
		sort.Slice(group, func(a, b int) bool { return group[a].x0 < group[b].x0 })
		cur := hline{y, group[0].x0, group[0].x1}
		for _, g := range group[1:] {
			if g.x0 <= cur.x1+join {
				cur.x1 = math.Max(cur.x1, g.x1)
				continue
			}
			out.h = append(out.h, cur)
			cur = hline{y, g.x0, g.x1}
		}
		out.h = append(out.h, cur)
		i = j
	}

	sort.Slice(r.v, func(i, j int) bool { return r.v[i].x < r.v[j].x })
	for i := 0; i < len(r.v); {
		j := i
		xs := 0.0
		for j < len(r.v) && r.v[j].x-r.v[i].x <= snap {
			xs += r.v[j].x
			j++
		}
		x := xs / float64(j-i)
		group := append([]vline(nil), r.v[i:j]...)
		sort.Slice(group, func(a, b int) bool { return group[a].y0 < group[b].y0 })
		cur := vline{x, group[0].y0, group[0].y1}
		for _, g := range group[1:] {
			if g.y0 <= cur.y1+join {
				cur.y1 = math.Max(cur.y1, g.y1)
				continue
			}
			out.v = append(out.v, cur)
			cur = vline{x, g.y0, g.y1}
		}
		out.v = append(out.v, cur)
		i = j
	}
	return out
}
