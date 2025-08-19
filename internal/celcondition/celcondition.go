package celcondition

import (
	"fmt"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
)

const (
	CostLimit               = 1_000
	InterruptCheckFrequency = 1000
)

func PrepareCondition(serviceName, celCondition string, valueType string) (cel.Program, error) {
	switch serviceName {
	case triggersrepo.ServiceSignal:
		return PrepareSignalCondition(celCondition, valueType)
	case triggersrepo.ServiceEvent:
		return PrepareEventCondition(celCondition)
	default:
		return nil, fmt.Errorf("unknown service name: %s", serviceName)
	}
}

func PrepareEventCondition(celCondition string) (cel.Program, error) {

	opts := []cel.EnvOption{
		cel.Variable("source", cel.StringType),
		cel.Variable("name", cel.StringType),
		cel.Variable("durationNs", cel.DynType),
		cel.Variable("metadata", cel.StringType),
		cel.Variable("previousSource", cel.StringType),
		cel.Variable("previousName", cel.StringType),
		cel.Variable("previousDurationNs", cel.DynType),
		cel.Variable("previousMetadata", cel.StringType),
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
	vars := map[string]any{
		"source":             "",
		"name":               "",
		"durationNs":         0,
		"metadata":           "",
		"previousSource":     "",
		"previousName":       "",
		"previousDurationNs": 0,
		"previousMetadata":   "",
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

func EvaluateEventCondition(prg cel.Program, event *vss.Event, previousEvent *vss.Event) (bool, error) {
	if event == nil {
		return false, fmt.Errorf("event is nil")
	}
	if previousEvent == nil {
		previousEvent = &vss.Event{}
	}
	vars := map[string]any{
		"source":             event.Source,
		"name":               event.Name,
		"durationNs":         event.DurationNs,
		"metadata":           event.Metadata,
		"previousSource":     previousEvent.Source,
		"previousName":       previousEvent.Name,
		"previousDurationNs": previousEvent.DurationNs,
		"previousMetadata":   previousEvent.Metadata,
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	return out.Type() == celtypes.BoolType && out.Value() == true, nil
}

func PrepareSignalCondition(celCondition string, valueType string) (cel.Program, error) {

	opts := []cel.EnvOption{
		cel.Variable("valueNumber", cel.DynType),
		cel.Variable("valueString", cel.StringType),
		cel.Variable("value", cel.DynType),
		cel.Variable("source", cel.DoubleType),
		cel.Variable("previousValueNumber", cel.DoubleType),
		cel.Variable("previousValueString", cel.StringType),
		cel.Variable("previousValue", cel.DynType),
		cel.Variable("previousSource", cel.StringType),
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
	prg, err := env.Program(ast,
		cel.CostLimit(CostLimit),
		cel.InterruptCheckFrequency(InterruptCheckFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to program CEL expression: %w", err)
	}
	vars := map[string]any{
		"valueNumber":         0,
		"valueString":         "",
		"source":              "",
		"previousValueNumber": 0,
		"previousValueString": "",
		"previousSource":      "",
	}

	switch valueType {
	case signals.NumberType:
		vars["value"] = 0
		vars["previousValue"] = 0
	case signals.StringType:
		vars["value"] = ""
		vars["previousValue"] = ""
	default:
		return nil, fmt.Errorf("unknown value type: %s", valueType)
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

func EvaluateSignalCondition(prg cel.Program, signal, previousSignal *vss.Signal, valueType string) (bool, error) {
	if signal == nil {
		return false, fmt.Errorf("signal is nil")
	}
	if previousSignal == nil {
		previousSignal = &vss.Signal{}
	}
	vars := map[string]any{
		"valueNumber":         signal.ValueNumber,
		"valueString":         signal.ValueString,
		"source":              signal.Source,
		"previousValueNumber": previousSignal.ValueNumber,
		"previousValueString": previousSignal.ValueString,
		"previousSource":      previousSignal.Source,
	}
	switch valueType {
	case signals.NumberType:
		vars["value"] = signal.ValueNumber
		vars["previousValue"] = previousSignal.ValueNumber
	case signals.StringType:
		vars["value"] = signal.ValueString
		vars["previousValue"] = previousSignal.ValueString
	default:
		return false, fmt.Errorf("unknown value type: %s", valueType)
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	return out.Type() == celtypes.BoolType && out.Value() == true, nil
}
