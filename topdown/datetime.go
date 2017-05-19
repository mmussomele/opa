package topdown

import (
	"strconv"
	"time"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/topdown/builtins"
)

func builtinDatetimeToInt(a ast.Value) (ast.Value, error) {
	d, ok := a.(ast.Datetime)
	if !ok {
		return nil, builtins.NewOperandTypeErr(1, a, ast.DatetimeTypeName)
	}
	i, _ := d.Int64()
	return ast.Number(strconv.FormatInt(i, 10)), nil
}

func builtinDatetimeFromInt(a ast.Value) (ast.Value, error) {
	i, ok := a.(ast.Number)
	if !ok {
		return nil, builtins.NewOperandTypeErr(1, a, ast.NumberTypeName)
	}
	j, _ := i.Int()
	t := time.Unix(int64(j), 0)
	return ast.Datetime(t), nil
}

func builtinNewDate(a, b, c ast.Value) (ast.Value, error) {
	x, ok1 := a.(ast.Number)
	y, ok2 := b.(ast.Number)
	z, ok3 := c.(ast.Number)

	if !ok1 {
		return nil, builtins.NewOperandTypeErr(1, a, ast.NumberTypeName)
	} else if !ok2 {
		return nil, builtins.NewOperandTypeErr(1, b, ast.NumberTypeName)
	} else if !ok3 {
		return nil, builtins.NewOperandTypeErr(1, c, ast.NumberTypeName)
	}

	year, _ := x.Int()
	month, _ := y.Int()
	day, _ := z.Int()
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return ast.Datetime(t), nil
}

func builtinNewTime(a, b, c ast.Value) (ast.Value, error) {
	x, ok1 := a.(ast.Number)
	y, ok2 := b.(ast.Number)
	z, ok3 := c.(ast.Number)

	if !ok1 {
		return nil, builtins.NewOperandTypeErr(1, a, ast.NumberTypeName)
	} else if !ok2 {
		return nil, builtins.NewOperandTypeErr(1, b, ast.NumberTypeName)
	} else if !ok3 {
		return nil, builtins.NewOperandTypeErr(1, c, ast.NumberTypeName)
	}

	hour, _ := x.Int()
	min, _ := y.Int()
	sec, _ := z.Int()
	t := time.Date(0, time.January, 0, hour, min, sec, 0, time.UTC)
	return ast.Datetime(t), nil
}

func builtinNewDatetime(a, b, c, d, e, f ast.Value) (ast.Value, error) {
	n1, ok1 := a.(ast.Number)
	n2, ok2 := a.(ast.Number)
	n3, ok3 := a.(ast.Number)
	n4, ok4 := a.(ast.Number)
	n5, ok5 := a.(ast.Number)
	n6, ok6 := a.(ast.Number)

	if !ok1 {
		return nil, builtins.NewOperandTypeErr(1, a, ast.NumberTypeName)
	} else if !ok2 {
		return nil, builtins.NewOperandTypeErr(1, b, ast.NumberTypeName)
	} else if !ok3 {
		return nil, builtins.NewOperandTypeErr(1, c, ast.NumberTypeName)
	} else if !ok4 {
		return nil, builtins.NewOperandTypeErr(1, d, ast.NumberTypeName)
	} else if !ok5 {
		return nil, builtins.NewOperandTypeErr(1, e, ast.NumberTypeName)
	} else if !ok6 {
		return nil, builtins.NewOperandTypeErr(1, f, ast.NumberTypeName)
	}

	year, _ := n1.Int()
	month, _ := n2.Int()
	day, _ := n3.Int()
	hour, _ := n4.Int()
	min, _ := n5.Int()
	sec, _ := n6.Int()
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	return ast.Datetime(t), nil
}

func init() {
	RegisterFunctionalBuiltin1(ast.DatetimeToInt.Name, builtinDatetimeToInt)
	RegisterFunctionalBuiltin1(ast.DatetimeFromInt.Name, builtinDatetimeFromInt)
	RegisterFunctionalBuiltin3(ast.NewDate.Name, builtinNewDate)
	RegisterFunctionalBuiltin3(ast.NewTime.Name, builtinNewTime)
	RegisterFunctionalBuiltin6(ast.NewDatetime.Name, builtinNewDatetime)
}
