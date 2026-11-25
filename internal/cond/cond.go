// Package cond evaluates OpenFGA relationship conditions (CEL expressions)
// so the trace walker can explain exactly why a conditional edge passed or
// failed. It mirrors OpenFGA's semantics: the tuple's persisted context takes
// precedence over the request context, and parameters are converted to the
// declared types before evaluation.
package cond

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// Status is the outcome of a condition evaluation.
type Status string

const (
	Met     Status = "met"
	NotMet  Status = "not-met"
	Missing Status = "missing-params"
	Failed  Status = "error"
)

// Result describes a condition evaluation.
type Result struct {
	Name       string
	Expression string
	Status     Status
	// Params are the effective parameter values used, rendered for display,
	// keyed by name. Source is "tuple" or "request".
	Params  []Param
	Missing []string
	Err     string
}

// Param is a parameter value used in an evaluation.
type Param struct {
	Name, Type, Value, Source string
}

// OK reports whether the condition was met.
func (r *Result) OK() bool { return r != nil && r.Status == Met }

// Summary is a one-line description.
func (r *Result) Summary() string {
	switch r.Status {
	case Met:
		return r.Name + " ✓"
	case NotMet:
		return r.Name + " ✗ (false)"
	case Missing:
		return r.Name + " ? missing " + strings.Join(r.Missing, ", ")
	default:
		return r.Name + " ! " + r.Err
	}
}

type compiled struct {
	def    *openfgav1.Condition
	prg    cel.Program
	params map[string]*openfgav1.ConditionParamTypeRef
	err    error
}

// Evaluator evaluates the conditions of one authorization model.
type Evaluator struct {
	conds map[string]*compiled
}

// New compiles all conditions in a model. Compilation errors are reported at
// evaluation time for the affected condition only.
func New(defs map[string]*openfgav1.Condition) *Evaluator {
	e := &Evaluator{conds: map[string]*compiled{}}
	for name, def := range defs {
		c := &compiled{def: def, params: def.GetParameters()}
		c.prg, c.err = compile(def)
		e.conds[name] = c
	}
	return e
}

func compile(def *openfgav1.Condition) (cel.Program, error) {
	opts := []cel.EnvOption{ipAddressLib()}
	for name, p := range def.GetParameters() {
		t, err := celType(p)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", name, err)
		}
		opts = append(opts, cel.Variable(name, t))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(def.GetExpression())
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if !reflect.DeepEqual(ast.OutputType(), cel.BoolType) {
		return nil, fmt.Errorf("expression must evaluate to bool, got %s", ast.OutputType())
	}
	return env.Program(ast)
}

func celType(p *openfgav1.ConditionParamTypeRef) (*cel.Type, error) {
	gen := func(i int) (*cel.Type, error) {
		if len(p.GetGenericTypes()) <= i {
			return cel.DynType, nil
		}
		return celType(p.GetGenericTypes()[i])
	}
	switch p.GetTypeName() {
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_ANY:
		return cel.DynType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_BOOL:
		return cel.BoolType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_STRING:
		return cel.StringType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_INT:
		return cel.IntType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_UINT:
		return cel.UintType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_DOUBLE:
		return cel.DoubleType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_DURATION:
		return cel.DurationType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_TIMESTAMP:
		return cel.TimestampType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_IPADDRESS:
		return ipAddressCELType, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_LIST:
		g, err := gen(0)
		if err != nil {
			return nil, err
		}
		return cel.ListType(g), nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_MAP:
		g, err := gen(0)
		if err != nil {
			return nil, err
		}
		return cel.MapType(cel.StringType, g), nil
	}
	return nil, fmt.Errorf("unsupported parameter type %s", p.GetTypeName())
}

// TypeName renders a parameter type, e.g. "list<string>".
func TypeName(p *openfgav1.ConditionParamTypeRef) string {
	n := strings.ToLower(strings.TrimPrefix(p.GetTypeName().String(), "TYPE_NAME_"))
	if len(p.GetGenericTypes()) > 0 {
		var g []string
		for _, t := range p.GetGenericTypes() {
			g = append(g, TypeName(t))
		}
		n += "<" + strings.Join(g, ", ") + ">"
	}
	return n
}

// Evaluate evaluates a named condition. tupleCtx comes from the tuple and
// takes precedence over reqCtx, matching OpenFGA.
func (e *Evaluator) Evaluate(name string, tupleCtx, reqCtx *structpb.Struct) *Result {
	c, ok := e.conds[name]
	if !ok {
		return &Result{Name: name, Status: Failed, Err: "condition not defined in model"}
	}
	res := &Result{Name: name, Expression: c.def.GetExpression()}
	if c.err != nil {
		res.Status, res.Err = Failed, c.err.Error()
		return res
	}
	vars := map[string]any{}
	names := make([]string, 0, len(c.params))
	for n := range c.params {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pt := c.params[n]
		raw, src := tupleCtx.GetFields()[n], "tuple"
		if raw == nil {
			raw, src = reqCtx.GetFields()[n], "request"
		}
		if raw == nil {
			res.Missing = append(res.Missing, n)
			continue
		}
		v, err := convert(raw.AsInterface(), pt)
		if err != nil {
			res.Status, res.Err = Failed, fmt.Sprintf("parameter %s: %v", n, err)
			return res
		}
		vars[n] = v
		res.Params = append(res.Params, Param{Name: n, Type: TypeName(pt), Value: display(raw), Source: src})
	}
	out, _, err := c.prg.Eval(vars)
	if err != nil {
		if len(res.Missing) > 0 && strings.Contains(err.Error(), "no such attribute") {
			res.Status = Missing
			return res
		}
		res.Status, res.Err = Failed, err.Error()
		return res
	}
	b, ok := out.Value().(bool)
	switch {
	case !ok:
		res.Status, res.Err = Failed, "expression did not return a bool"
	case b:
		res.Status = Met
	default:
		res.Status = NotMet
	}
	// Parameters not referenced by the evaluated branch do not make a result unknown.
	res.Missing = nil
	return res
}

func display(v *structpb.Value) string {
	if s, ok := v.GetKind().(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	b, err := v.MarshalJSON()
	if err != nil {
		return fmt.Sprint(v.AsInterface())
	}
	return string(b)
}

func convert(v any, pt *openfgav1.ConditionParamTypeRef) (any, error) {
	switch pt.GetTypeName() {
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_ANY:
		return v, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_BOOL:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool, got %T", v)
		}
		return b, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_STRING:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", v)
		}
		return s, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_INT:
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return nil, fmt.Errorf("expected int, got %v", v)
		}
		return int64(f), nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_UINT:
		f, ok := v.(float64)
		if !ok || f < 0 || f != float64(uint64(f)) {
			return nil, fmt.Errorf("expected uint, got %v", v)
		}
		return uint64(f), nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_DOUBLE:
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("expected double, got %T", v)
		}
		return f, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_DURATION:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected duration string, got %T", v)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, err
		}
		return d, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_TIMESTAMP:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected RFC 3339 timestamp string, got %T", v)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, err
		}
		return t, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_IPADDRESS:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected IP address string, got %T", v)
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		return IPAddress{Addr: a}, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_LIST:
		l, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected list, got %T", v)
		}
		if len(pt.GetGenericTypes()) == 0 {
			return l, nil
		}
		out := make([]any, len(l))
		for i, x := range l {
			c, err := convert(x, pt.GetGenericTypes()[0])
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = c
		}
		return out, nil
	case openfgav1.ConditionParamTypeRef_TYPE_NAME_MAP:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected map, got %T", v)
		}
		if len(pt.GetGenericTypes()) == 0 {
			return m, nil
		}
		out := make(map[string]any, len(m))
		for k, x := range m {
			c, err := convert(x, pt.GetGenericTypes()[0])
			if err != nil {
				return nil, fmt.Errorf("[%q]: %w", k, err)
			}
			out[k] = c
		}
		return out, nil
	}
	return nil, errors.New("unsupported parameter type")
}

// IPAddress is OpenFGA's custom CEL "ipaddress" type.
type IPAddress struct{ Addr netip.Addr }

var ipAddressCELType = cel.OpaqueType("ipaddress")

var _ ref.Val = IPAddress{}

func (a IPAddress) ConvertToNative(t reflect.Type) (any, error) {
	if t == reflect.TypeOf(a) {
		return a, nil
	}
	if t.Kind() == reflect.String {
		return a.Addr.String(), nil
	}
	return nil, fmt.Errorf("cannot convert ipaddress to %s", t)
}

func (a IPAddress) ConvertToType(t ref.Type) ref.Val {
	switch t {
	case ipAddressCELType:
		return a
	case types.StringType:
		return types.String(a.Addr.String())
	}
	return types.NewErr("cannot convert ipaddress to %s", t.TypeName())
}

func (a IPAddress) Equal(other ref.Val) ref.Val {
	o, ok := other.(IPAddress)
	return types.Bool(ok && o.Addr == a.Addr)
}

func (a IPAddress) Type() ref.Type { return ipAddressCELType }
func (a IPAddress) Value() any     { return a }

func ipAddressLib() cel.EnvOption {
	return cel.Lib(ipLib{})
}

type ipLib struct{}

func (ipLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Types(ipAddressCELType),
		cel.Function("ipaddress",
			cel.Overload("string_to_ipaddress", []*cel.Type{cel.StringType}, ipAddressCELType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					s, ok := v.Value().(string)
					if !ok {
						return types.NewErr("ipaddress requires a string")
					}
					a, err := netip.ParseAddr(s)
					if err != nil {
						return types.NewErr("invalid ipaddress %q: %v", s, err)
					}
					return IPAddress{Addr: a}
				}))),
		cel.Function("in_cidr",
			cel.MemberOverload("ipaddress_in_cidr_string", []*cel.Type{ipAddressCELType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
					a, ok := lhs.(IPAddress)
					if !ok {
						return types.NewErr("in_cidr requires an ipaddress receiver")
					}
					s, ok := rhs.Value().(string)
					if !ok {
						return types.NewErr("in_cidr requires a string CIDR")
					}
					p, err := netip.ParsePrefix(s)
					if err != nil {
						return types.NewErr("invalid CIDR %q: %v", s, err)
					}
					return types.Bool(p.Contains(a.Addr))
				}))),
	}
}

func (ipLib) ProgramOptions() []cel.ProgramOption { return nil }
