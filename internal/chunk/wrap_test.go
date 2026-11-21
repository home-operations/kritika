package chunk

import (
	"slices"
	"testing"
)

// TestStatementsKeepApartFromTheDeclarationsTheyHold: a statement with no
// name of its own widens the one declaration it holds only when it holds
// nothing else, so a conditional that runs code before defining a function
// stays apart from it.
func TestStatementsKeepApartFromTheDeclarationsTheyHold(t *testing.T) {
	type want struct {
		symbol      string
		start, end  int
		head        int
		wantPresent bool
	}
	tests := []struct {
		name, path, src string
		want            []want
	}{
		{
			name: "python conditional with a call before its function", path: "boot.py",
			src: `import os

if feature.enabled:
    initialize()
    def boot():
        return 1
`,
			want: []want{{"boot", 5, 6, 5, true}, {"", 3, 6, 3, true}, {"boot", 3, 6, 5, false}},
		},
		{
			name: "python try with a handler that defines a function", path: "guard.py",
			src: `try:
    import fast
except ImportError:
    fast = None
    def run():
        return 1
`,
			want: []want{{"run", 5, 6, 5, true}, {"run", 1, 6, 5, false}},
		},
		{
			name: "python decorator still widens", path: "deco.py",
			src: `@cache
def f():
    pass
`,
			want: []want{{"f", 1, 3, 2, true}},
		},
		{
			name: "c++ linkage specification still widens", path: "ext.cpp",
			src: `extern "C"
int add(int a) {
  return a;
}
`,
			want: []want{{"add", 1, 4, 2, true}},
		},
		{
			name: "typescript exported ambient class still widens", path: "box.d.ts",
			src: `export declare class Box {
  get(): number;
}
`,
			want: []want{{"Box", 1, 3, 1, true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := (&Parser{}).Parse(tt.path, []byte(tt.src))
			defer f.Close()
			for _, w := range tt.want {
				found := slices.ContainsFunc(f.Decls, func(d Decl) bool {
					return d.Symbol == w.symbol && d.StartLine == w.start && d.EndLine == w.end && d.HeadLine == w.head
				})
				if found != w.wantPresent {
					t.Errorf("declaration %q L%d-%d head L%d present = %v, want %v in %+v", w.symbol, w.start, w.end, w.head, found, w.wantPresent, f.Decls)
				}
			}
		})
	}
}
