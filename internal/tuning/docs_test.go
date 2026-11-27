package tuning

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the tuning reference in "+docsPage)

// docsPage is the page whose region between docsBegin and docsEnd is the
// reference that TestDocs generates.
const (
	docsPage  = "../../docs/src/content/docs/configuration.md"
	docsBegin = "<!-- tuning:begin: generated from internal/tuning by TestDocs; rewrite with go test ./internal/tuning/ -run Docs -update -->"
	docsEnd   = "<!-- tuning:end -->"
)

// TestDocs holds the configuration page's reference of every tunable field
// to the package's source: the area's and the field's doc comments and the
// envDefault tag.
func TestDocs(t *testing.T) {
	want := reference(t)
	page, err := os.ReadFile(docsPage)
	if err != nil {
		t.Fatal(err)
	}
	before, rest, ok := bytes.Cut(page, []byte(docsBegin))
	if !ok {
		t.Fatalf("%s has no %s marker", docsPage, docsBegin)
	}
	got, after, ok := bytes.Cut(rest, []byte(docsEnd))
	if !ok {
		t.Fatalf("%s has no %s marker after %s", docsPage, docsEnd, docsBegin)
	}
	if string(got) == want {
		return
	}
	if !*update {
		t.Fatalf("%s: the tuning reference differs from internal/tuning's fields; run go test ./internal/tuning/ -run Docs -update and commit the page", docsPage)
	}
	out := bytes.Join([][]byte{before, []byte(docsBegin), []byte(want), []byte(docsEnd), after}, nil)
	if err := os.WriteFile(docsPage, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// reference is the generated region: a heading and table for each area, in
// Values' order, as the repository's markdown formatter lays them out.
func reference(t *testing.T) string {
	t.Helper()
	typeDocs, fieldDocs := sourceDocs(t)
	var b strings.Builder
	b.WriteString("\n\n")
	v := reflect.ValueOf(Defaults())
	for i := range v.NumField() {
		area := v.Type().Field(i)
		key, _, _ := strings.Cut(area.Tag.Get("yaml"), ",")
		doc, ok := typeDocs[area.Type.Name()]
		if !ok {
			t.Errorf("type %s has no doc comment", area.Type.Name())
		}
		fmt.Fprintf(&b, "#### `tuning.%s`\n\n%s\n\n", key, cell(doc))
		rows := [][]string{{"Setting", "Environment variable", "Default", "Description"}}
		walk(v.Field(i), "tuning."+key+".", func(l leaf) {
			desc, ok := fieldDocs[area.Type.Name()+"."+l.field.Name]
			if !ok {
				t.Errorf("%s.%s has no doc comment", area.Type.Name(), l.field.Name)
			}
			desc = cell(desc)
			if l.field.Tag.Get("tuning") == "zero" {
				if l.value.Kind() == reflect.Slice {
					desc += " An empty list, and 0, are allowed."
				} else {
					desc += " 0 is allowed."
				}
			}
			rows = append(rows, []string{
				"`" + l.path + "`",
				"`" + l.env + "`",
				"`" + l.field.Tag.Get("envDefault") + "`",
				desc,
			})
		})
		writeTable(&b, rows)
		b.WriteString("\n")
	}
	return b.String()
}

// cell is a doc comment as one line of a table cell or paragraph: its lines
// joined, and the characters that markdown would read as table syntax or
// HTML escaped.
func cell(doc string) string {
	s := strings.Join(strings.Fields(doc), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

// writeTable writes rows, the first the header, with every column padded to
// its widest cell, the shape the markdown formatter gives a table.
func writeTable(b *strings.Builder, rows [][]string) {
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c), 3)
		}
	}
	line := func(r []string) {
		for i, c := range r {
			fmt.Fprintf(b, "| %s%s ", c, strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)))
		}
		b.WriteString("|\n")
	}
	line(rows[0])
	sep := make([]string, len(widths))
	for i, w := range widths {
		sep[i] = strings.Repeat("-", w)
	}
	line(sep)
	for _, r := range rows[1:] {
		line(r)
	}
}

// sourceDocs reads the package's non-test files for the doc comment of each
// struct type, by name, and of each of its fields, by Type.Field. A field
// without a comment of its own shares the one above it, as in
//
//	// A and B are ...
//	A int
//	B int
func sourceDocs(t *testing.T) (types, fields map[string]string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	types, fields = map[string]string{}, map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				if doc != nil {
					types[ts.Name.Name] = doc.Text()
				}
				var shared *ast.CommentGroup
				prevLine := 0
				for _, fld := range st.Fields.List {
					if fld.Doc != nil {
						shared = fld.Doc
					} else if fset.Position(fld.Pos()).Line != prevLine+1 {
						shared = nil
					}
					prevLine = fset.Position(fld.End()).Line
					if shared == nil {
						continue
					}
					for _, n := range fld.Names {
						fields[ts.Name.Name+"."+n.Name] = shared.Text()
					}
				}
			}
		}
	}
	return types, fields
}
