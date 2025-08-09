package celcondition

import (
	"fmt"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
)

func PrepareCondition(celCondition string) (cel.Program, error) {
	opts := []cel.EnvOption{
		cel.Variable("valueNumber", cel.DoubleType),
		cel.Variable("valueString", cel.StringType),
		cel.CrossTypeNumericComparisons(true),
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to compile CEL expression: %w", err)
	}
	ast, issues := env.Compile(celCondition)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("failed to program CEL expression: %w", err)
	}
	vars := map[string]interface{}{
		"valueNumber": 0,
		"valueString": "",
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	if out.Type() != celtypes.BoolType {
		return nil, fmt.Errorf("output type is not bool: %s", out.Type())
	}
	return prg, nil
}

func EvaluateCondition(prg cel.Program, signal *vss.Signal) (bool, error) {
	if signal == nil {
		return false, fmt.Errorf("signal is nil")
	}
	vars := map[string]interface{}{
		"valueNumber": signal.ValueNumber,
		"valueString": signal.ValueString,
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	return out.Type() == celtypes.BoolType && out.Value() == true, nil
}
