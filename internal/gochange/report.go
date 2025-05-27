package gochange

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

func report(pass *analysis.Pass, fn *ssa.Function, param *ssa.Parameter, match match) {
	switch m := match.(type) {
	case declaredType:
		if types.Identical(param.Type().Underlying(), m.typ.Type().Underlying()) {
			return
		}
		localName, importEdit := resolveTypeReference(pass, param, m.typ)
		start, end := typePositionRange(param)
		edits := []analysis.TextEdit{{Pos: start, End: end, NewText: []byte(localName)}}
		if importEdit != nil {
			edits = append(edits, *importEdit)
		}
		pass.Report(analysis.Diagnostic{Pos: param.Pos(), Message: fmt.Sprintf("%s could be %s", param.Name(), types.TypeString(m.typ.Type(), types.RelativeTo(pass.Pkg))), SuggestedFixes: []analysis.SuggestedFix{{Message: fmt.Sprintf("Change %s to %s", param.Name(), localName), TextEdits: edits}}})
	case anonymousIface:
		if types.Identical(param.Type().Underlying(), m.iface.Underlying()) {
			return
		}
		if m.iface.IsMethodSet() {
			reportRenameParamType(pass, param, types.TypeString(m.iface, func(pkg *types.Package) string {
				if pkg == pass.Pkg {
					return ""
				}
				return pkg.Name()
			}))
			return
		}
		if containsComparable(m.iface) {
			handleGenericInterface(pass, fn, param, m.iface)
		} else {
			interfaceStr := buildInterfaceString(pass, m.iface)
			reportRenameParamType(pass, param, interfaceStr)
		}
	case decalaredTypeUnion:
		if types.Identical(param.Type().Underlying(), m.typ.Underlying()) {
			return
		}
		handleGenericInterface(pass, fn, param, m.typ)
	default:
		panic(fmt.Sprintf("unknown match type %T", match))
	}
}

func containsComparable(iface *types.Interface) bool {
	for t := range iface.EmbeddedTypes() {
		if types.Comparable(t) {
			return true
		}
	}
	return false
}

func buildInterfaceString(pass *analysis.Pass, typ types.Type) string {
	if typ, isTypeParam := typ.(*types.TypeParam); isTypeParam {
		if typ, isNamed := typ.Constraint().(*types.Named); isNamed {
			return typ.Obj().Name()
		}
	}

	var components []string
	iface := typ.Underlying().(*types.Interface)
	for t := range iface.EmbeddedTypes() {
		components = append(components, types.TypeString(t, types.RelativeTo(pass.Pkg)))
	}
	for m := range iface.ExplicitMethods() {
		components = append(components, methodString(pass, m))
	}
	return "interface{" + strings.Join(components, ";") + "}"
}

func handleGenericInterface(pass *analysis.Pass, fn *ssa.Function, param *ssa.Parameter, iface types.Type) {
	funcDecl := fn.Syntax().(*ast.FuncDecl)
	constraint := buildInterfaceString(pass, iface)
	start, end := typePositionRange(param)

	// TODO: what if T exists already?
	typePramName := "T"
	typeParamText := ""
	if funcDecl.Type.TypeParams == nil {
		typeParamText = fmt.Sprintf("[%s %s]", typePramName, constraint)
	} else {
		typeParamText = fmt.Sprintf(", %s %s", typePramName, constraint)
	}

	edits := []analysis.TextEdit{{
		Pos:     funcDecl.Name.End(),
		End:     funcDecl.Name.End(),
		NewText: []byte(typeParamText),
	}, {
		Pos:     start,
		End:     end,
		NewText: []byte(typePramName),
	}}

	pass.Report(analysis.Diagnostic{
		Pos:     param.Pos(),
		Message: fmt.Sprintf("%s could be %s", param.Name(), constraint),
		SuggestedFixes: []analysis.SuggestedFix{{
			Message:   "Convert to generic function",
			TextEdits: edits,
		}},
	})
}

// resolveTypeReference resolves the type reference to its local name or fully qualified name and
// returns any edits needed to add the import statement if necessary.
func resolveTypeReference(pass *analysis.Pass, param *ssa.Parameter, typ *types.TypeName) (string, *analysis.TextEdit) {
	if typ.Pkg() == nil || typ.Pkg() == pass.Pkg {
		return typ.Name(), nil
	}

	var file_with_imports *ast.File
	paramPos := pass.Fset.Position(param.Pos())
	for _, file := range pass.Files {
		if pass.Fset.Position(file.Pos()).Filename == paramPos.Filename {
			file_with_imports = file
		}
	}
	if file_with_imports == nil {
		panic("file not found")
	}

	for _, imp := range file_with_imports.Imports {
		if imp.Path.Value == fmt.Sprintf(`"%s"`, typ.Pkg().Path()) {
			if imp.Name == nil {
				return fmt.Sprintf("%s.%s", typ.Pkg().Name(), typ.Name()), nil
			} else if imp.Name.Name == "_" {
				return typ.Name(), nil
			} else {
				return fmt.Sprintf("%s.%s", imp.Name.Name, typ.Name()), nil
			}
		}
	}

	return fmt.Sprintf("%s.%s", typ.Pkg().Name(), typ.Name()), createImportEdit(file_with_imports, typ.Pkg())
}

func createImportEdit(file *ast.File, pkg *types.Package) *analysis.TextEdit {
	var impDecl *ast.GenDecl
	for _, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			impDecl = genDecl
			break
		}
	}

	if impDecl != nil && impDecl.Lparen.IsValid() {
		return &analysis.TextEdit{
			Pos:     impDecl.Rparen - 1,
			End:     impDecl.Rparen - 1,
			NewText: []byte(fmt.Sprintf("\n\t\"%s\"\n", pkg.Path())),
		}
	} else {
		return &analysis.TextEdit{
			Pos:     file.Name.End() + 1, // first line after the package declaration
			End:     file.Name.End() + 1,
			NewText: []byte(fmt.Sprintf("import \"%s\"\n", pkg.Path())),
		}
	}

}

func typePositionRange(param *ssa.Parameter) (start, end token.Pos) {
	fn := param.Parent()
	if fn == nil {
		panic("parameter has no parent function")
	}

	syntax := fn.Syntax()
	if syntax == nil {
		panic("function has no syntax")
	}

	if tparam, ok := param.Type().(*types.TypeParam); ok {
		return findTypeParamPosition(syntax, tparam.Obj().Name())
	} else {
		return findParamPosition(syntax, param)
	}

}

func findTypeParamPosition(syntax ast.Node, name string) (start, end token.Pos) {
	var typeParams *ast.FieldList
	switch fn := syntax.(type) {
	case *ast.FuncDecl:
		typeParams = fn.Type.TypeParams
	case *ast.FuncLit:
		typeParams = fn.Type.TypeParams
	default:
		panic("unsupported function type")
	}

	if typeParams == nil {
		panic("function has no type parameters")
	}

	for _, field := range typeParams.List {
		for _, ident := range field.Names {
			if ident.Name == name {
				return field.Type.Pos(), field.Type.End()
			}
		}
	}
	panic("type parameter not found")
}

func findParamPosition(syntax ast.Node, param *ssa.Parameter) (start, end token.Pos) {
	var fields []*ast.Field
	switch fn := syntax.(type) {
	case *ast.FuncDecl:
		if fn.Recv != nil && param == param.Parent().Params[0] {
			fields = fn.Recv.List
		} else {
			fields = fn.Type.Params.List
		}
	case *ast.FuncLit:
		fields = fn.Type.Params.List
	default:
		panic("unsupported function type")
	}

	paramPos := param.Pos()
	for _, field := range fields {
		for _, name := range field.Names {
			if name.Pos() == paramPos {
				return field.Type.Pos(), field.Type.End()
			}
		}
	}
	panic("parameter position not found")
}

func reportRenameParamType(pass *analysis.Pass, param *ssa.Parameter, newType string) {
	start, end := typePositionRange(param)
	pass.Report(analysis.Diagnostic{
		Pos:     param.Pos(),
		Message: fmt.Sprintf("%s could be %s", param.Name(), newType),
		SuggestedFixes: []analysis.SuggestedFix{{
			Message: "Simplify type",
			TextEdits: []analysis.TextEdit{{
				Pos:     start,
				End:     end,
				NewText: []byte(newType),
			}},
		}},
	})
}

func methodString(pass *analysis.Pass, m *types.Func) string {
	return m.Name() + strings.TrimPrefix(types.TypeString(m.Type().(*types.Signature), types.RelativeTo(pass.Pkg)), "func")
}
