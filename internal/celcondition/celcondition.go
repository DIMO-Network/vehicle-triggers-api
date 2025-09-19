package celcondition

import (
	"fmt"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/jftuga/geodist"
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

func toFloat64(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case int16:
		return float64(v)
	case int8:
		return float64(v)
	case float32:
		return float64(v)
	case uint64:
		return float64(v)
	case uint32:
		return float64(v)
	case uint16:
		return float64(v)
	case uint8:
		return float64(v)
	case uint:
		return float64(v)
	}
	return 0
}

func geoDistanceOpt() cel.EnvOption {
	return cel.Function("geoDistance",
		cel.Overload("geoDistance_double_double_double_double",
			[]*cel.Type{cel.DynType, cel.DynType, cel.DynType, cel.DynType},
			cel.DoubleType,
			cel.FunctionBinding(func(values ...ref.Val) ref.Val {
				coord1Lat := toFloat64(values[0].Value())
				coord1Lon := toFloat64(values[1].Value())
				coord2Lat := toFloat64(values[2].Value())
				coord2Lon := toFloat64(values[3].Value())

				coord1 := geodist.Coord{Lat: coord1Lat, Lon: coord1Lon}
				coord2 := geodist.Coord{Lat: coord2Lat, Lon: coord2Lon}
				_, km := geodist.HaversineDistance(coord1, coord2)
				return celtypes.Double(km)
			}),
		),
	)
}

func PrepareSignalCondition(celCondition string, valueType string) (cel.Program, error) {
	opts := []cel.EnvOption{
		cel.Variable("valueNumber", cel.DynType),
		cel.Variable("valueString", cel.StringType),
		cel.Variable("value", cel.DynType),
		cel.Variable("value.Latitude", cel.DynType),
		cel.Variable("value.Longitude", cel.DynType),
		cel.Variable("value.HDOP", cel.DynType),
		geoDistanceOpt(),
		cel.Variable("source", cel.DoubleType),
		cel.Variable("previousValueNumber", cel.DoubleType),
		cel.Variable("previousValueString", cel.StringType),
		cel.Variable("previousValue", cel.DynType),
		cel.Variable("previousValue.Latitude", cel.DynType),
		cel.Variable("previousValue.Longitude", cel.DynType),
		cel.Variable("previousValue.HDOP", cel.DynType),
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
	case signals.LocationType:
		vars["value.Latitude"] = 0
		vars["value.Longitude"] = 0
		vars["value.HDOP"] = 0
		vars["previousValue.Latitude"] = 0
		vars["previousValue.Longitude"] = 0
		vars["previousValue.HDOP"] = 0
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
	case signals.LocationType:
		vars["value.Latitude"] = signal.ValueLocation.Latitude
		vars["value.Longitude"] = signal.ValueLocation.Longitude
		vars["value.HDOP"] = signal.ValueLocation.HDOP
		vars["previousValue.Latitude"] = previousSignal.ValueLocation.Latitude
		vars["previousValue.Longitude"] = previousSignal.ValueLocation.Longitude
		vars["previousValue.HDOP"] = previousSignal.ValueLocation.HDOP
	default:
		return false, fmt.Errorf("unknown value type: %s", valueType)
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	return out.Type() == celtypes.BoolType && out.Value() == true, nil
}
