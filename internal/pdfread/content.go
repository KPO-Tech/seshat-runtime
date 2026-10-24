package pdf

import "strings"

// contentReader interprets the content stream of a page, and of the forms it draws, and collects the text and the rectangles.
type contentReader struct {
	g      gstate
	gstack []gstate
	enc    TextEncoding
	text   []Text
	rect   []Rect
}

// Content returns the page's content.
func (p Page) Content() Content {
	// Handle in case the content page is empty
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return Content{}
	}
	r := &contentReader{enc: &nopEncoder{}, g: gstate{Th: 1, CTM: ident}}
	r.interpret(p.V.Key("Contents"), p.Resources(), 0)
	return Content{r.text, r.rect}
}

// interpret reads one content stream. res are the resources the stream draws its fonts and objects from: the page's, then a form's own.
// The q and Q of a stream belong to it, so a form cannot restore a state it did not save.
func (r *contentReader) interpret(strm, res Value, depth int) {
	outer := r.gstack
	r.gstack = nil
	Interpret(strm, func(stk *Stack, op string) {
		n := stk.Len()
		args := make([]Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		r.operator(op, args, res, depth)
	})
	r.gstack = outer
}

func (r *contentReader) operator(op string, args []Value, res Value, depth int) {
	switch op {
	case "cm", "re", "q", "Q", "Do":
		r.graphicsOperator(op, args, res, depth)
	case "BT", "ET", "T*", "TD", "Td", "Tm":
		r.positionOperator(op, args)
	case "Tc", "Tw", "Tz", "TL", "Tr", "Ts", "Tf":
		r.stateOperator(op, args, res)
	case "\"", "'", "Tj", "TJ":
		r.showOperator(op, args)
	}
}

func matrixOf(args []Value) matrix {
	var m matrix
	for i := 0; i < 6; i++ {
		m[i/2][i%2] = args[i].Float64()
	}
	m[2][2] = 1
	return m
}

func (r *contentReader) graphicsOperator(op string, args []Value, res Value, depth int) {
	switch op {
	case "cm": // update the current transformation matrix
		if len(args) != 6 {
			panic("bad g.Tm")
		}
		r.g.CTM = matrixOf(args).mul(r.g.CTM)

	case "re": // append rectangle to path
		if len(args) != 4 {
			panic("bad re")
		}
		x, y, w, h := args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64()
		r.rect = append(r.rect, Rect{Point{x, y}, Point{x + w, y + h}})

	case "q": // save graphics state
		r.gstack = append(r.gstack, r.g)

	case "Q": // restore graphics state
		n := len(r.gstack) - 1
		r.g = r.gstack[n]
		r.gstack = r.gstack[:n]

	case "Do": // draw an object: a form is a content stream of its own, with its own matrix and resources
		r.drawForm(args, res, depth)
	}
}

// maxFormDepth bounds how deep forms may draw forms: a malformed file must not recurse for ever.
const maxFormDepth = 8

func (r *contentReader) drawForm(args []Value, res Value, depth int) {
	if len(args) != 1 || depth >= maxFormDepth {
		return
	}
	form := res.Key("XObject").Key(args[0].Name())
	if form.Key("Subtype").Name() != "Form" {
		return
	}
	saved, savedEnc := r.g, r.enc
	if m := form.Key("Matrix"); m.Kind() == Array && m.Len() == 6 {
		var fm matrix
		for i := 0; i < 6; i++ {
			fm[i/2][i%2] = m.Index(i).Float64()
		}
		fm[2][2] = 1
		r.g.CTM = fm.mul(r.g.CTM)
	}
	formRes := form.Key("Resources")
	if formRes.IsNull() {
		formRes = res
	}
	r.interpret(form, formRes, depth+1)
	r.g, r.enc = saved, savedEnc
}

func (r *contentReader) positionOperator(op string, args []Value) {
	switch op {
	case "BT": // begin text (reset text matrix and line matrix)
		r.g.Tm = ident
		r.g.Tlm = r.g.Tm

	case "ET": // end text

	case "T*": // move to start of next line
		r.nextLine()

	case "TD": // move text position and set leading
		if len(args) != 2 {
			panic("bad Td")
		}
		r.g.Tl = -args[1].Float64()
		r.moveText(args)

	case "Td": // move text position
		r.moveText(args)

	case "Tm": // set text matrix and line matrix
		if len(args) != 6 {
			panic("bad g.Tm")
		}
		m := matrixOf(args)
		r.g.Tm = m
		r.g.Tlm = m
	}
}

func (r *contentReader) nextLine() {
	x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -r.g.Tl, 1}}
	r.g.Tlm = x.mul(r.g.Tlm)
	r.g.Tm = r.g.Tlm
}

func (r *contentReader) moveText(args []Value) {
	if len(args) != 2 {
		panic("bad Td")
	}
	tx := args[0].Float64()
	ty := args[1].Float64()
	x := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
	r.g.Tlm = x.mul(r.g.Tlm)
	r.g.Tm = r.g.Tlm
}

func (r *contentReader) stateOperator(op string, args []Value, res Value) {
	if len(args) != 1 && op != "Tf" {
		panic("bad " + op)
	}
	switch op {
	case "Tc": // set character spacing
		r.g.Tc = args[0].Float64()
	case "Tw": // set word spacing
		r.g.Tw = args[0].Float64()
	case "Tz": // set horizontal text scaling
		r.g.Th = args[0].Float64() / 100
	case "TL": // set text leading
		r.g.Tl = args[0].Float64()
	case "Tr": // set text rendering mode
		r.g.Tmode = int(args[0].Int64())
	case "Ts": // set text rise
		r.g.Trise = args[0].Float64()
	case "Tf": // set text font and size
		if len(args) != 2 {
			panic("bad TL")
		}
		r.g.Tf = Font{res.Key("Font").Key(args[0].Name()), nil}
		r.enc = r.g.Tf.Encoder()
		if r.enc == nil {
			if DebugOn {
				println("no cmap for", args[0].Name())
			}
			r.enc = &nopEncoder{}
		}
		r.g.Tfs = args[1].Float64()
	}
}

func (r *contentReader) showOperator(op string, args []Value) {
	switch op {
	case "\"": // set spacing, move to next line, and show text
		if len(args) != 3 {
			panic("bad \" operator")
		}
		r.g.Tw = args[0].Float64()
		r.g.Tc = args[1].Float64()
		r.nextLine()
		r.showText(args[2].RawString())

	case "'": // move to next line and show text
		if len(args) != 1 {
			panic("bad ' operator")
		}
		r.nextLine()
		r.showText(args[0].RawString())

	case "Tj": // show text
		if len(args) != 1 {
			panic("bad Tj operator")
		}
		r.showText(args[0].RawString())

	case "TJ": // show text, allowing individual glyph positioning
		v := args[0]
		for i := 0; i < v.Len(); i++ {
			x := v.Index(i)
			if x.Kind() == String {
				r.showText(x.RawString())
			} else {
				tx := -x.Float64() / 1000 * r.g.Tfs * r.g.Th
				r.g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(r.g.Tm)
			}
		}
		r.showText("\n")
	}
}

// show appends one glyph at the current text position and moves the position by its advance.
func (r *contentReader) show(ch string, w0 float64) {
	f := r.g.Tf.BaseFont()
	if i := strings.Index(f, "+"); i >= 0 {
		f = f[i+1:]
	}

	Trm := matrix{{r.g.Tfs * r.g.Th, 0, 0}, {0, r.g.Tfs, 0}, {0, r.g.Trise, 1}}.mul(r.g.Tm).mul(r.g.CTM)
	r.text = append(r.text, Text{f, Trm[0][0], Trm[2][0], Trm[2][1], w0 / 1000 * Trm[0][0], ch})

	tx := w0/1000*r.g.Tfs + r.g.Tc
	tx *= r.g.Th
	r.g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(r.g.Tm)
}

func (r *contentReader) showText(s string) {
	if r.g.Tf.isCID() {
		// A composite font reads two bytes for each glyph, and its widths are in the descendant font, by CID.
		for i := 0; i+1 < len(s); i += 2 {
			code := int(s[i])<<8 | int(s[i+1])
			r.show(r.enc.Decode(s[i:i+2]), r.g.Tf.cidWidth(code))
		}
		return
	}
	n := 0
	decoded := r.enc.Decode(s)
	for _, ch := range decoded {
		var w0 float64
		if n < len(s) {
			w0 = r.g.Tf.Width(int(s[n]))
		}
		n++
		r.show(string(ch), w0)
	}
}
