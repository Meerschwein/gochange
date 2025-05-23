package gochange

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"
)

type constraint interface{ constraintImpl() }

type calledMethod struct{ meth *types.Func }
type exact struct{ war types.Type }
type cantChange struct{}
type anyIntConstant struct{}

func (calledMethod) constraintImpl()   {}
func (exact) constraintImpl()          {}
func (cantChange) constraintImpl()     {}
func (anyIntConstant) constraintImpl() {}

func collectConstraints(param *ssa.Parameter, node ssa.Value) []constraint {
	var constraints []constraint

	for _, instr := range *node.Referrers() {
		switch instr := instr.(type) {
		case *ssa.Call:
			constraints = append(constraints, collectConstraintsCall(instr, param, node)...)
		case *ssa.FieldAddr:
			// we need to check whether the field is only used as the reciever in methods calls
			// otherwise we cannot chacnge the type
			methods := isOnlyARecieverInMethodCalls(instr)
			if methods == nil {
				constraints = append(constraints, cantChange{})
				continue
			}
			for _, m := range *methods {
				constraints = append(constraints, calledMethod{m})
			}
		case *ssa.Field:
			methods := isOnlyARecieverInMethodCalls(instr)
			if methods == nil {
				constraints = append(constraints, cantChange{})
			} else {
				for _, m := range *methods {
					constraints = append(constraints, calledMethod{m})
				}
			}
		case *ssa.MakeInterface:
			constraints = append(constraints, collectConstraints(param, instr)...)
		case *ssa.UnOp:
			switch instr.Op {
			case token.MUL: // pointer indirection
				constraints = append(constraints, collectConstraints(param, instr)...)
			default:
				constraints = append(constraints, cantChange{})
			}
		case *ssa.Store:
			constraints = append(constraints, collectConstraintsStore(instr, param, node)...)
		case *ssa.MakeClosure:
			fn := instr.Fn.(*ssa.Function)
			freeVarPos := slices.Index(instr.Bindings, node)
			if freeVarPos == -1 {
				panic("free var not found")
			} else if len(fn.FreeVars) != len(instr.Bindings) {
				panic("FreeVars/Bindings mismatch")
			}
			constraints = append(constraints, collectConstraints(param, fn.FreeVars[freeVarPos])...)
		case *ssa.IndexAddr:
			switch x := instr.X.(type) {
			case *ssa.UnOp:
				assertMul(x)
				switch inner := x.X.(type) {
				case *ssa.Global, *ssa.FreeVar, *ssa.Parameter, *ssa.Alloc:
					typ := inner.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Slice).Elem()
					return []constraint{exact{typ}}
				case *ssa.FieldAddr:
					typ := inner.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(inner.Field).Type().Underlying().(*types.Slice).Elem()
					return []constraint{exact{typ}}
				default:
					panic(fmt.Sprintf("unknown type %T", inner))
				}
			case *ssa.Alloc:
				typ := x.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Array).Elem()
				return []constraint{exact{typ}}
			case *ssa.Parameter:
				// this is a slice parameter that gets indexed so it cannot change
				return []constraint{cantChange{}}
			case *ssa.Slice, *ssa.MakeSlice:
				typ := x.Type().Underlying().(*types.Slice).Elem()
				return []constraint{exact{typ}}
			case *ssa.Lookup:
				if instr.Index == param {
					return []constraint{cantChange{}}
				}
				unop := x.X.(*ssa.UnOp)
				assertMul(unop)
				switch inner := unop.X.(type) {
				case *ssa.Global, *ssa.FreeVar, *ssa.Parameter:
					typ := inner.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Map).Elem().Underlying().(*types.Slice).Elem()
					return []constraint{exact{typ}}
				case *ssa.FieldAddr:
					typ := inner.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(inner.Field).Type().Underlying().(*types.Map).Elem().Underlying().(*types.Slice).Elem()
					return []constraint{exact{typ}}
				default:
					panic(fmt.Sprintf("unknown type %T", inner))
				}
			default:
				panic(fmt.Sprintf("unknown type %T", x))
			}
		case *ssa.BinOp:
			switch instr.Op {
			case token.ADD, token.SUB, token.MUL, token.QUO:
				if xConst, ok := instr.X.(*ssa.Const); ok {
					if xBasic, ok := xConst.Type().(*types.Basic); ok {
						if xBasic.Info()&types.IsNumeric != 0 {
							constraints = append(constraints, anyIntConstant{})
							continue
						}
					}
				}
				if yConst, ok := instr.Y.(*ssa.Const); ok {
					if yBasic, ok := yConst.Type().(*types.Basic); ok {
						if yBasic.Info()&types.IsNumeric != 0 {
							constraints = append(constraints, anyIntConstant{})
							continue
						}
					}
				}
				constraints = append(constraints, cantChange{})
			default:
				constraints = append(constraints, cantChange{})
			}
		case *ssa.Return:
			returnPos := slices.Index(instr.Results, node)
			typ := instr.Parent().Signature.Results().At(returnPos).Type()
			constraints = append(constraints, exact{typ})
		case *ssa.ChangeInterface:
			typ := instr.Type()
			constraints = append(constraints, exact{typ})
		default:
			// panic(fmt.Sprintf("unknown type %T", instr))
		}
	}

	return constraints
}

func collectConstraintsStore(store *ssa.Store, param *ssa.Parameter, node ssa.Value) []constraint {
	switch addr := store.Addr.(type) {
	case *ssa.Alloc:
		if addr == node {
			return []constraint{}
		}
		return collectConstraints(param, addr)
	case *ssa.FieldAddr:
		typ := addr.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(addr.Field).Type()
		return []constraint{exact{typ}}
	case *ssa.IndexAddr:
		return collectConstraints(param, addr.X)
	case *ssa.Parameter:
		fn := addr.Parent()
		// Call.Args includes the receiver if it exists but Signiture.Params() does not
		// so we need to adjust the index
		paramPos := slices.Index(fn.Params, addr)
		if isMethod(fn.Signature) && paramPos == 0 { // we are the receiver
			return []constraint{calledMethod{fn.Object().(*types.Func)}}
		}
		if isMethod(fn.Signature) { // we are a parameter so skip the reveiver
			paramPos -= 1
		}
		typ := fn.Signature.Params().At(paramPos).Type()
		return []constraint{exact{typ}}
	case *ssa.UnOp:
		assertMul(addr)
		if addr == node {
			return []constraint{}
		}
		return collectConstraints(param, addr.X)
	case *ssa.FreeVar, *ssa.Global:
		typ := addr.Type().Underlying().(*types.Pointer).Elem().Underlying()
		return []constraint{exact{typ}}
	default:
		panic(fmt.Sprintf("unknown store type %T", addr))
	}
}

func collectConstraintsCall(call *ssa.Call, param *ssa.Parameter, node ssa.Value) []constraint {
	// This is either a method call or a paramter to a function

	methodCallOnInterface := call.Call.IsInvoke()
	if methodCallOnInterface && call.Call.Value == node { // we are the receiver
		return []constraint{calledMethod{call.Call.Method}}
	}

	paramPos := getParamPos(call, node)

	if methodCallOnInterface { // we are a parameter
		typ := call.Call.Signature().Params().At(paramPos).Type()
		return []constraint{exact{typ}}
	}

	// "call" mode: when Method is nil (!IsInvoke), a CallCommon represents an ordinary function callcom of the
	// value in Value, which may be a *Builtin, a *Function or any other value of kind 'func'.
	switch callcom := call.Call.Value.(type) {
	// a *Function, indicating a statically dispatched call to a package-level function, an anonymous function, or a method of a named type.
	case *ssa.Function:
		// Call.Args includes the receiver if it exists but Signiture.Params() does not
		// so we need to adjust the index
		if isMethod(callcom.Signature) && paramPos == 0 { // we are the receiver
			return []constraint{calledMethod{callcom.Object().(*types.Func)}}
		} else if isMethod(callcom.Signature) { // we are a parameter so skip the reveiver
			paramPos -= 1
		}
		typ := callcom.Signature.Params().At(paramPos).Origin().Type()
		return []constraint{exact{typ}}
	// a *MakeClosure, indicating an immediately applied function literal with free variables.
	case *ssa.MakeClosure:
		typ := callcom.Fn.(*ssa.Function).Signature.Params().At(paramPos).Type()
		return []constraint{exact{typ}}
	// a *Builtin, indicating a statically dispatched call to a built-in function.
	case *ssa.Builtin:
		return []constraint{cantChange{}}
	// any other value, indicating a dynamically dispatched function call.
	case *ssa.UnOp: // MUL is pointer indirection (load)
		assertMul(callcom)
		switch x := callcom.X.(type) {
		case *ssa.Global, *ssa.Alloc:
			// we load a function to call
			typ := x.Type().(*types.Pointer).Elem().(*types.Signature).Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		case *ssa.IndexAddr:
			// global slice
			switch x := x.X.(type) {
			case *ssa.Global, *ssa.FieldAddr:
				typ := x.Type().Underlying().(*types.Pointer).Elem().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			case *ssa.UnOp:
				assertMul(x)
				typ := x.X.Type().Underlying().(*types.Pointer).Elem().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			case *ssa.Phi:
				for _, e := range x.Edges {
					switch e := e.(type) {
					case *ssa.Const, *ssa.Slice:
						typ := e.Type().Underlying().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
						return []constraint{exact{typ}}
					case *ssa.Call:
						if _, isBuiltin := e.Call.Value.(*ssa.Builtin); !isBuiltin {
							panic("not a builtin")
						}
						paramPos := slices.Index(e.Call.Args, ssa.Value(param))
						if paramPos == -1 {
							// a function call that we a re not involved in so we are only interested in the result
							typ := e.Call.Signature().Results().At(0).Type().Underlying().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(0).Type()
							return []constraint{exact{typ}}
						} else {
							panic("param involved in phi node call")
						}
					default:
						panic(fmt.Sprintf("unknown type %T", e))
					}
				}
			case *ssa.Slice: // we call a function in a slice
				typ := x.Type().Underlying().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			case *ssa.Call: // this call returns a slice of functions
				typ := x.Call.Signature().Results().At(0).Type().Underlying().(*types.Slice).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			default:
				panic(fmt.Sprintf("unknown type %T", x))
			}
		case *ssa.FieldAddr: // func field in a struct
			sig := x.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(x.Field).Type().Underlying().(*types.Signature)
			typ := sig.Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		case *ssa.FreeVar: // we are a free variable in a closure in a call to a var function
			sig := x.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Signature)
			typ := sig.Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		case *ssa.Call: // we are a parameter to a function returned from this call
			typ := call.Call.Signature().Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		default:
			// we load a pointer to ourself
			return collectConstraints(param, callcom.X)
		}
	case *ssa.Parameter: // val is the function to which we are a parameter
		typ := callcom.Type().Underlying().(*types.Signature).Params().At(paramPos).Type()
		return []constraint{exact{typ}}
	case *ssa.Extract: // we are calling a fucntion whose type was the resuilt of type assertion
		switch target := callcom.Tuple.(type) {
		case *ssa.TypeAssert:
			typ := target.AssertedType.(*types.Signature).Params().At(callcom.Index).Type()
			return []constraint{exact{typ}}
		case *ssa.Lookup:
			switch x := target.X.(type) {
			case *ssa.UnOp:
				assertMul(x)
				globl, isGlobal := x.X.(*ssa.Global)
				if !isGlobal {
					panic("not a global")
				}
				typ := globl.Type().Underlying().(*types.Pointer).Elem().(*types.Map).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			default:
				panic(fmt.Sprintf("unknown type %T", x))
			}
		case *ssa.Call:
			sig := target.Call.Value.(*ssa.Function).Signature.Results().At(callcom.Index).Type().Underlying().(*types.Signature)
			typ := sig.Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		default:
			panic(fmt.Sprintf("unknown type %T", target))
		}
	case *ssa.TypeAssert:
		typ := callcom.AssertedType.(*types.Signature).Params().At(paramPos).Type()
		return []constraint{exact{typ}}
	case *ssa.Lookup:
		// the function is the value in a map
		switch x := callcom.X.(type) {
		case *ssa.UnOp:
			assertMul(x)
			globl, isGlobal := x.X.(*ssa.Global)
			if !isGlobal {
				panic("not a global")
			}
			typ := globl.Type().Underlying().(*types.Pointer).Elem().(*types.Map).Elem().Underlying().(*types.Signature).Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		default:
			panic(fmt.Sprintf("unknown type %T", x))
		}
	case *ssa.Call:
		// we are an arguemnt to  an fn call of a function returned from another fucntion
		switch cval := callcom.Call.Value.(type) {
		case *ssa.Function:
			typ := cval.Signature.Results().At(0).Type().Underlying().(*types.Signature).Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		case *ssa.UnOp:
			faddr := cval.X.(*ssa.FieldAddr)
			switch t := faddr.X.(type) {
			case *ssa.Alloc, *ssa.Parameter:
				s := t.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct)
				fn := s.Field(faddr.Field).Type().Underlying()
				typ := fn.(*types.Signature).Results().At(0).Type().Underlying().(*types.Signature).Params().At(paramPos).Type()
				return []constraint{exact{typ}}
			default:
				panic(fmt.Sprintf("unknown type %T", t))
			}
		default:
			panic(fmt.Sprintf("unknown type %T", cval))
		}
	case *ssa.Phi:
		// it is conditional which function we call
		for _, e := range callcom.Edges {
			fn := e.(*ssa.Function)
			typ := fn.Signature.Params().At(paramPos).Type()
			return []constraint{exact{typ}}
		}
	default:
		panic(fmt.Sprintf("unknown type %T", callcom))
	}

	return []constraint{}
}

// HELPERS

func isOnlyARecieverInMethodCalls(val ssa.Value) *[]*types.Func {
	var funcs []*types.Func

	for _, instr := range *val.Referrers() {
		switch instr := instr.(type) {
		case *ssa.Call:
			common := instr.Call
			// "invoke" mode: when Method is non-nil (IsInvoke), a CallCommon represents a dynamically dispatched call to an interface method.
			if common.IsInvoke() {
				funcs = append(funcs, common.Method)
				continue
			}

			// "call" mode: when Method is nil (!IsInvoke), a CallCommon represents an ordinary function call of the
			// value in Value, which may be a *Builtin, a *Function or any other value of kind 'func'.
			switch fval := common.Value.(type) {
			// a *Function, indicating a statically dispatched call to a package-level function,
			// an anonymous function, or a method of a named type.
			case *ssa.Function:
				if isMethod(common.Signature()) {
					xPos := slices.Index(common.Args, val)
					if xPos == -1 {
						panic("xPos not found")
					}
					if xPos == 0 { // we are the receiver
						funcs = append(funcs, fval.Object().(*types.Func))
					} else { // we are a parameter
						return nil
					}
				}
			// a *MakeClosure, indicating an immediately applied function literal with free variables.
			case *ssa.MakeClosure:
				panic("MakeClosure")
			// a *Builtin, indicating a statically dispatched call to a built-in function.
			case *ssa.Builtin:
				panic("Builtin")
			// any other value, indicating a dynamically dispatched function call.
			default:
				panic(fmt.Sprintf("unknown type %T", val))
			}
		default:
			return nil
		}
	}
	return &funcs
}

func isMethod(sig *types.Signature) bool {
	return sig.Recv() != nil
}

func getParamPos(call *ssa.Call, param ssa.Value) int {
	pos := slices.Index(call.Call.Args, param)
	if pos == -1 {
		panic("param not found")
	}
	return pos
}

func assertMul(unop *ssa.UnOp) {
	if unop.Op != token.MUL {
		panic(fmt.Sprintf("unknown op %s", unop.Op))
	}
}
