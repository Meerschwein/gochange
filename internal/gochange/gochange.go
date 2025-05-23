package gochange

import (
	"bytes"
	"fmt"
	"go/types"
	"iter"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

var Analyzer = &analysis.Analyzer{
	Name:     "gochange",
	Doc:      "check for better types in function signatures using SSA analysis",
	Run:      run,
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
}

func run(pass *analysis.Pass) (any, error) {
	ssaInfo := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	thisPkg := ssaInfo.Pkg.Pkg

	for _, imp := range thisPkg.Imports() {
		// These packages allow for strange things to happen that are hard to analyze, skip for now
		switch imp.Path() {
		case "runtime", "unsafe", "reflect":
			return nil, nil
		}
	}

	knownIfaces := collectInterfaces(thisPkg)

	for _, fn := range ssaInfo.SrcFuncs {
		analyzeFn(pass, fn, knownIfaces)
	}

	return nil, nil
}

func analyzeFn(pass *analysis.Pass, fn *ssa.Function, knownIfaces []*types.TypeName) {
	if isAnonymousFn(fn) || // don't analyze closures
		fn.Blocks == nil || // External function so no function body to analyze
		hasSpecialSig(fn) {
		return
	}

	// If the function has a receiver, skip it (the first parameter is the receiver)
	isMethod := fn.Signature.Recv() != nil
	params := fn.Params
	if isMethod {
		params = params[1:]
	}

	if len(params) == 0 { // No parameters to analyze
		return
	}

	for _, param := range params {
		// skip functions and empty interfaces
		if _, isFn := param.Type().Underlying().(*types.Signature); isFn {
			continue
		} else if iface, isIface := param.Type().(*types.Interface); isIface && iface.Empty() {
			continue
		}

		constraints := collectConstraints(param, param)
		if len(constraints) == 0 {
			continue
		}

		match := findMatch(knownIfaces, constraints, isMethod)
		if match == nil { // No match for constraints
			continue
		}

		report(pass, fn, param, match)
	}
}

// ANALYSIS

var stdPkgs []*types.Package

func init() {
	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadTypes}, "std")
	if err != nil {
		panic(fmt.Sprintf("Failed to load std packages: %v", err))
	}
	stdPkgs = make([]*types.Package, len(pkgs))
	for i, pkg := range pkgs {
		stdPkgs[i] = pkg.Types
	}
}

func collectInterfaces(pkg *types.Package) []*types.TypeName {
	ifaces := []*types.TypeName{}

	for obj := range allObjects(pkg) {
		iface := getIfaceTypeName(obj)
		if iface != nil {
			ifaces = append(ifaces, obj.(*types.TypeName))
		}
	}

	for _, pkg := range pkg.Imports() {
		for obj := range allObjects(pkg) {
			if !obj.Exported() {
				continue
			}
			iface := getIfaceTypeName(obj)
			if iface != nil {
				ifaces = append(ifaces, iface)
			}
		}
	}

	for _, pkg := range stdPkgs {
		for obj := range allObjects(pkg) {
			if !obj.Exported() {
				continue
			}
			iface := getIfaceTypeName(obj)
			if iface != nil {
				ifaces = append(ifaces, iface)
			}
		}
	}

	return ifaces
}

func getIfaceTypeName(obj types.Object) *types.TypeName {
	// An interface is a [types.TypeName] with underlying type of [types.Interface]
	// This resolves typealiases

	if !types.IsInterface(obj.Type()) {
		return nil
	}

	typeObj, _ := obj.(*types.TypeName)
	return typeObj
}

func hasSpecialSig(fn *ssa.Function) bool {
	name := fn.Name()
	sig := fn.Signature
	nParams := sig.Params().Len()

	// Using a string for the Type seems to be sketchy but [types.TypeString] resolves aliases
	// and normalizes import names so it is actually fine
	firstParamType := ""
	if nParams > 0 {
		firstParamType = types.TypeString(sig.Params().At(0).Type(), nil)
	}

	// func TestXxx(t *testing.T) https://pkg.go.dev/testing#pkg-overview
	if strings.HasPrefix(name, "Test") && nParams == 1 && firstParamType == "*testing.T" {
		return true
	}

	// func BenchmarkXxx(b *testing.B) https://pkg.go.dev/testing#hdr-Benchmarks
	if strings.HasPrefix(name, "Benchmark") && nParams == 1 && firstParamType == "*testing.B" {
		return true
	}

	// func FuzzXxx(f *testing.F) https://pkg.go.dev/testing#hdr-Fuzzing
	if strings.HasPrefix(name, "Fuzz") && nParams == 1 && firstParamType == "*testing.F" {
		return true
	}

	// func TestMain(m *testing.M) https://pkg.go.dev/testing#hdr-Main
	if name == "TestMain" && nParams == 1 && firstParamType == "*testing.M" {
		return true
	}

	// func ExampleXxx() https://pkg.go.dev/testing#hdr-Examples
	// This one doesn't even have any parameters to analyze but its here for completeness
	if strings.HasPrefix(name, "Example") && nParams == 0 {
		return true
	}

	return false
}

// HELPERS

func allObjects(pkg *types.Package) iter.Seq[types.Object] {
	return func(yield func(types.Object) bool) {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			if !yield(scope.Lookup(name)) {
				return
			}
		}
	}
}

func isAnonymousFn(fn *ssa.Function) bool {
	return strings.Contains(fn.Name(), "$")
}

func printSsa(ssaThing any) {
	buf := &bytes.Buffer{}
	switch ssaThing := ssaThing.(type) {
	case *ssa.Function:
		ssa.WriteFunction(buf, ssaThing)
	case *ssa.Package:
		ssa.WritePackage(buf, ssaThing)
	case ssa.Instruction:
		fmt.Printf("ssa type: %T", ssaThing)
		buf.WriteString(ssaThing.String())
	case *[]ssa.Instruction:
		for _, instr := range *ssaThing {
			printSsa(instr)
		}
	default:
		panic(fmt.Sprintf("Unknown ssa type: %T", ssaThing))
	}
	println(buf.String())
}
