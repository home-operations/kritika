package chunk

import (
	"slices"

	"github.com/odvcencio/gotreesitter"
)

const (
	// kindImport is the kind of a package clause or a statement that imports
	// or includes code.
	kindImport = "import"
	// kindOther is the kind of a statement that names no kind of its own.
	kindOther = "other"
)

var (
	cIncludes  = []string{"preproc_include"}
	cppImports = []string{"preproc_include", "using_declaration"}
)

// importTypes lists, by grammar, the node types of the top-level statements
// that import or include code but that kindFromType does not take for an
// import by their names. Each holds only for the grammars it is listed
// under: JavaScript's using_declaration and C#'s using_statement bind a
// disposable value instead.
var importTypes = map[string][]string{
	"c":       cIncludes,
	"glsl":    cIncludes,
	"cpp":     cppImports,
	"cuda":    cppImports,
	"arduino": cppImports,
	"hlsl":    cppImports,
	"c_sharp": {"using_directive"},
	"rust":    {"extern_crate_declaration"},
	"julia":   {"using_statement"},
	"erlang":  {"pp_include", "pp_include_lib"},
	"ocaml":   {"open_module"},
	"dart":    {"part_directive"},
	// PHP's are expressions, inside an expression_statement.
	"php": {"require_expression", "require_once_expression", "include_expression", "include_once_expression"},
}

// importCalls lists, by grammar, the methods whose call without a receiver
// at the top level imports code.
var importCalls = map[string][]string{
	"ruby": {"require", "require_relative"},
}

// topLevelKind is the kind of n, a statement at the top level of src of type
// typ.
func topLevelKind(lp *languageParser, n *gotreesitter.Node, typ string, src []byte) string {
	kind := kindFromType(typ)
	if kind != kindOther {
		return kind
	}
	if typ == "expression_statement" && n.NamedChildCount() == 1 {
		typ = n.NamedChild(0).Type(lp.lang)
	}
	if slices.Contains(importTypes[lp.entry.Name], typ) {
		return kindImport
	}
	if calls := importCalls[lp.entry.Name]; calls != nil && typ == "call" && n.ChildByFieldName("receiver", lp.lang) == nil {
		if m := n.ChildByFieldName("method", lp.lang); m != nil && slices.Contains(calls, m.Text(src)) {
			return kindImport
		}
	}
	return kind
}
