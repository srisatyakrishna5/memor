package extract

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"

	"github.com/memor-dev/memor/internal/graph"
)

// maxSignature bounds a rendered signature. A 400-character generic constraint
// is a wall of noise in the projection and costs tokens for nothing.
const maxSignature = 180

// Symbol is one extracted declaration together with the names its body calls.
// The call list is resolved later, once every file has been parsed.
type Symbol struct {
	Node  *graph.Node
	Calls []string
}

// GoSymbols extracts function, method, type, and exported value declarations
// from a Go source file using the standard library parser.
//
// go/ast is chosen over tree-sitter deliberately: tree-sitter's Go bindings are
// mostly C and require CGO, which would break the cross-compiled binaries the
// npm installer ships.
func GoSymbols(rel, hash string, data []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	var out []Symbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			out = append(out, goFunc(fset, rel, hash, d))
		case *ast.GenDecl:
			out = append(out, goValues(fset, rel, hash, d)...)
		}
	}
	return out, nil
}

func goFunc(fset *token.FileSet, rel, hash string, d *ast.FuncDecl) Symbol {
	name := d.Name.Name
	kind := "func"
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = "method"
		if recv := receiverName(d.Recv.List[0].Type); recv != "" {
			name = recv + "." + name
		}
	}

	span := spanOf(fset, rel, hash, d.Pos(), d.End())
	// The body is the largest part of a declaration and the part a span already
	// points at, so the signature is rendered without it.
	body := d.Body
	doc := d.Doc
	d.Body, d.Doc = nil, nil
	signature := render(fset, d)
	d.Body, d.Doc = body, doc

	node := graph.SymNode(rel, name, signature, kind, span)
	return Symbol{Node: node, Calls: callNames(body)}
}

func goValues(fset *token.FileSet, rel, hash string, d *ast.GenDecl) []Symbol {
	var out []Symbol
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			span := spanOf(fset, rel, hash, s.Pos(), s.End())
			signature := "type " + s.Name.Name + " " + typeSummary(fset, s.Type)
			out = append(out, Symbol{
				Node: graph.SymNode(rel, s.Name.Name, signature, "type", span),
			})
		case *ast.ValueSpec:
			// Unexported constants and variables are implementation detail; the
			// file summary already covers them.
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			for _, ident := range s.Names {
				if !ident.IsExported() {
					continue
				}
				span := spanOf(fset, rel, hash, s.Pos(), s.End())
				out = append(out, Symbol{
					Node: graph.SymNode(rel, ident.Name, d.Tok.String()+" "+ident.Name, d.Tok.String(), span),
				})
			}
		}
	}
	return out
}

// typeSummary keeps a struct or interface to its head line. The member list is
// in the file the span points at.
func typeSummary(fset *token.FileSet, expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StructType:
		return fmt.Sprintf("struct{%d fields}", len(t.Fields.List))
	case *ast.InterfaceType:
		return fmt.Sprintf("interface{%d methods}", len(t.Methods.List))
	default:
		return render(fset, expr)
	}
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	default:
		return ""
	}
}

// callNames collects the identifiers a function body invokes. Selector calls
// contribute only the method name; the resolver decides whether a matching
// definition exists locally and drops the call otherwise.
func callNames(body *ast.BlockStmt) []string {
	if body == nil {
		return nil
	}
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			names = append(names, fun.Name)
		case *ast.SelectorExpr:
			names = append(names, fun.Sel.Name)
		}
		return true
	})
	return dedupe(names)
}

func spanOf(fset *token.FileSet, rel, hash string, start, end token.Pos) *graph.Span {
	from := fset.Position(start)
	to := fset.Position(end)
	return &graph.Span{
		Path:  rel,
		Start: from.Offset,
		End:   to.Offset,
		L0:    from.Line,
		L1:    to.Line,
		Hash:  hash,
	}
}

func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	out := strings.Join(strings.Fields(buf.String()), " ")
	if len(out) > maxSignature {
		out = out[:maxSignature] + "…"
	}
	return out
}
