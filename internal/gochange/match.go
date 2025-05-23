package gochange

import (
	"fmt"
	"go/types"
)

type match interface{ matchImpl() }

type declaredType struct{ typ *types.TypeName }
type decalaredTypeUnion struct{ typ *types.TypeParam }
type anonymousIface struct{ iface *types.Interface }

func (declaredType) matchImpl()       {}
func (decalaredTypeUnion) matchImpl() {}
func (anonymousIface) matchImpl()     {}

func findMatch(knownIfaces []*types.TypeName, constraints []constraint, isMethod bool) match {
	structs := []*types.TypeName{}
	ifaces := []*types.TypeName{}
	anonymousIfaces := []*types.Interface{}
	calledMethods := []*types.Func{}
	typeUnions := []*types.TypeParam{}

	for _, c := range constraints {
		switch c := c.(type) {
		case exact:
			switch t := c.war.Underlying().(type) {
			case *types.Pointer:
				// if _, isIface := t.Elem().(*types.Interface); isIface {
				// 	return nil // we cannot change the type
				// }
				return nil

			case *types.Slice, *types.Basic, *types.Chan, *types.Map:
				return nil

			case *types.Struct:
				switch t := c.war.(type) {
				case *types.Named:
					structs = append(structs, t.Obj())
				case *types.Struct: // anonymous struct
					return nil
				case *types.Alias:
					structs = append(structs, t.Obj())
				default:
					panic(fmt.Sprintf("struct is of unexpected type %+v", t))
				}

			case *types.Interface: // anonymous interface
				if t, isNamed := c.war.(*types.Named); isNamed {
					ifaces = append(ifaces, t.Obj())
					continue
				}
				if t.Empty() { // never suggest the empty interface
					continue
				}
				if t.IsMethodSet() { // anonymous interface
					anonymousIfaces = append(anonymousIfaces, t)
					continue
				}
				if t, isTypeParam := c.war.(*types.TypeParam); isTypeParam { // union constraint
					if _, isNamed := t.Constraint().(*types.Named); isNamed {
						typeUnions = append(typeUnions, t)
					} else {
						anonymousIfaces = append(anonymousIfaces, t.Underlying().(*types.Interface))
					}
					continue
				}

				anonymousIfaces = append(anonymousIfaces, t)

			default:
				// panic(fmt.Sprintf("unknown type %T", t))
			}

		case calledMethod:
			calledMethods = append(calledMethods, c.meth)

		case cantChange:
			return nil
		case anyIntConstant:
			typeUnions = append(typeUnions, anyIntTypeParam())
		default:
			panic(fmt.Sprintf("unknown type %T", c))
		}
	}

	if len(structs) > 0 {
		return nil
	}

	ifaceOfCalledMethods := types.NewInterfaceType(calledMethods, nil).Complete()

	for _, union := range typeUnions {
		if !types.Satisfies(union.Underlying().(*types.Interface), ifaceOfCalledMethods) {
			continue
		}
		for _, ifaceToCheck := range ifaces {
			if !types.Satisfies(
				union,
				ifaceToCheck.Type().Underlying().(*types.Interface)) {
				goto noMatch3
			}
		}
		// we can only match a type union if we are not a parameter to a method
		if !isMethod {
			return decalaredTypeUnion{union}
		}
	noMatch3:
	}

	for _, iface := range ifaces {
		if !types.Satisfies(iface.Type().Underlying().(*types.Interface), ifaceOfCalledMethods) {
			continue
		}
		for _, ifaceToCheck := range ifaces {
			if !types.Satisfies(
				iface.Type().Underlying().(*types.Interface),
				ifaceToCheck.Type().Underlying().(*types.Interface)) {
				goto noMatch
			}
		}
		return declaredType{iface}
	noMatch:
	}

	if len(anonymousIfaces) > 0 {
		return anonymousIface{anonymousIfaces[0]}
	}

	ifaceMethods := []*types.Func{}
	for _, iface := range ifaces {
		ifaceTypI := iface.Type().Underlying().(*types.Interface)
		if ifaceTypI.Empty() {
			continue
		}
		if ifaceTypI.IsMethodSet() {
			for m := range ifaceTypI.Methods() {
				ifaceMethods = append(ifaceMethods, m)
			}
		}
	}

	ifaceMethods = unique(ifaceMethods)
	calledMethods = unique(calledMethods)
	ifaceOfCalledMethodsAndIfaces := types.NewInterfaceType(append(ifaceMethods, calledMethods...), nil).Complete()

	if ifaceOfCalledMethodsAndIfaces.Empty() {
		return nil
	}

	for _, iface := range knownIfaces {
		if !types.Identical(ifaceOfCalledMethodsAndIfaces, iface.Type().Underlying().(*types.Interface)) {
			continue
		}
		for _, ifaceToCheck := range ifaces {
			if !types.Satisfies(
				iface.Type().Underlying().(*types.Interface),
				ifaceToCheck.Type().Underlying().(*types.Interface)) {
				goto noMatch2
			}
		}
		return declaredType{iface}
	noMatch2:
	}

	// create interface of called methods and return
	if len(ifaces) == 0 && len(typeUnions) == 0 {
		ifaceOfCalledMethodsAndIfaces = types.NewInterfaceType(calledMethods, nil).Complete()
		return anonymousIface{ifaceOfCalledMethodsAndIfaces}
	}

	return nil
}

func anyIntTypeParam() *types.TypeParam {
	terms := []*types.Term{
		types.NewTerm(true, types.Typ[types.Int]),
		types.NewTerm(true, types.Typ[types.Int8]),
		types.NewTerm(true, types.Typ[types.Int16]),
		types.NewTerm(true, types.Typ[types.Int32]),
		types.NewTerm(true, types.Typ[types.Int64]),
		types.NewTerm(true, types.Typ[types.Uint]),
		types.NewTerm(true, types.Typ[types.Uint8]),
		types.NewTerm(true, types.Typ[types.Uint16]),
		types.NewTerm(true, types.Typ[types.Uint32]),
		types.NewTerm(true, types.Typ[types.Uint64]),
		types.NewTerm(true, types.Typ[types.Uintptr]),
		types.NewTerm(true, types.Typ[types.Float32]),
		types.NewTerm(true, types.Typ[types.Float64]),
	}

	union := types.NewUnion(terms)

	return types.NewTypeParam(types.NewTypeName(0, nil, "blah", nil), union)
}

func unique[T comparable](slice []T) []T {
	seen := make(map[T]struct{})
	var uniqueSlice []T
	for _, v := range slice {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			uniqueSlice = append(uniqueSlice, v)
		}
	}
	return uniqueSlice
}
