package chunk

import (
	"slices"
	"testing"
)

// TestLeadNodesSeparatedByUnicodeSpace: lead nodes on one line take any
// white space between them, as the ends of the line do, so comments that a
// no-break space separates still lead into the declaration below.
func TestLeadNodesSeparatedByUnicodeSpace(t *testing.T) {
	tests := []struct {
		name, src string
	}{
		{name: "no-break space", src: "/* a */ /* b */\nfunction f() {}\n"},
		{name: "ideographic space", src: "/* a */　/* b */\nfunction f() {}\n"},
		{name: "ascii space", src: "/* a */ /* b */\nfunction f() {}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Parser{}).Parse("demo.js", []byte(tt.src))
			defer f.Close()
			if !slices.ContainsFunc(f.Decls, func(d Decl) bool {
				return d.Symbol == "f" && d.StartLine == 1 && d.StartByte == 0 && d.HeadLine == 2
			}) {
				t.Errorf("f does not start at its comments on line 1: %+v", f.Decls)
			}
		})
	}
}
