// Package signals provides a way to get signal definitions for use with webhooks.
// This will panic if the signal definitions cannot be loaded on startup.
package signals

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/DIMO-Network/model-garage/pkg/schema"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
)

const (
	NumberType   string = "float64"
	StringType   string = "string"
	LocationType string = "vss.Location"
)

func init() {
	err := loadSignalDefs()
	if err != nil {
		panic(fmt.Errorf("failed to load signal definitions: %w", err))
	}
}

var loadLock sync.RWMutex
var signalDefs []SignalDefinition
var signalMap map[string]SignalDefinition
var privilegeMap = map[string]string{
	"VEHICLE_NON_LOCATION_DATA":    "privilege:GetNonLocationHistory",
	"VEHICLE_ALL_TIME_LOCATION":    "privilege:GetLocationHistory",
	"VEHICLE_APPROXIMATE_LOCATION": "privilege:GetApproximateLocation",
}

// VSSPrefix is the schema prefix for VSS signals. When service is "signals",
// the metricName is stored as "vss.<signalName>" (e.g. "vss.speed").
const VSSPrefix = "vss."

// BareSignalName strips the schema prefix (e.g. "vss.") from a metric name,
// returning the raw signal name used in the schema lookup.
func BareSignalName(metricName string) string {
	return strings.TrimPrefix(metricName, VSSPrefix)
}

// DefaultPermissions is used for unknown signals (and events) when no schema definition exists.
var DefaultPermissions = []string{
	"privilege:GetNonLocationHistory",
	"privilege:GetLocationHistory",
}

// DefaultDefinition returns a signal definition for an unknown signal name, e.g. when the signal
// is not in the schema. valueType should be NumberType, StringType, or LocationType.
func DefaultDefinition(name, valueType string) SignalDefinition {
	return SignalDefinition{
		Name:        name,
		ValueType:  valueType,
		Permissions: DefaultPermissions,
	}
}

// SignalDefinition describes a telemetry signal available for use with webhooks.
type SignalDefinition struct {
	// Name is the JSON-safe name of the signal.
	Name string `json:"name"`
	// Description briefly explains what the signal represents.
	Description string `json:"description"`
	// Unit is the unit of measurement for the signal value (if any).
	Unit string `json:"unit"`
	// ValueType is the data type for the value field e.g. "float64" or "string"
	ValueType string `json:"valueType"`
	// Permissions is the permission required to access the signal.
	Permissions []string `json:"permissions"`
}

// GetSignalDefinition returns the signal definition for the given name.
func GetSignalDefinition(name string) (SignalDefinition, error) {
	loadLock.RLock()
	defer loadLock.RUnlock()

	signal, ok := signalMap[name]
	if !ok {
		return SignalDefinition{}, richerrors.Error{
			ExternalMsg: "signal definition not found",
			Code:        http.StatusNotFound,
		}
	}
	return signal, nil
}

// GetSignalDefinitionOrDefault returns the signal definition if found in the schema,
// otherwise a default definition with the given valueType and default permissions.
func GetSignalDefinitionOrDefault(name, valueType string) SignalDefinition {
	def, err := GetSignalDefinition(name)
	if err == nil {
		return def
	}
	return DefaultDefinition(name, valueType)
}

// GetAllSignalDefinitions returns a copy of the signal definitions.
func GetAllSignalDefinitions() []SignalDefinition {
	loadLock.RLock()
	defer loadLock.RUnlock()
	signalDefsCopy := make([]SignalDefinition, len(signalDefs))
	copy(signalDefsCopy, signalDefs)
	return signalDefsCopy
}

func loadSignalDefs() error {
	loadLock.Lock()
	defer loadLock.Unlock()

	defs, err := schema.LoadDefinitionFile(strings.NewReader(schema.DefaultDefinitionsYAML()))
	if err != nil {
		return fmt.Errorf("failed to load default schema definitions: %w", err)
	}
	signalInfo, err := schema.LoadSignalsCSV(strings.NewReader(schema.VssRel42DIMO()))
	if err != nil {
		return fmt.Errorf("failed to load default signal info: %w", err)
	}
	definedSignals := defs.DefinedSignal(signalInfo)
	signalMap = make(map[string]SignalDefinition, len(definedSignals))
	signalDefs = make([]SignalDefinition, 0, len(definedSignals))
	for _, signal := range definedSignals {
		def := SignalDefinition{
			Name:        signal.JSONName,
			ValueType:   signal.GOType(),
			Unit:        signal.Unit,
			Description: signal.Desc,
		}
		for _, privilege := range signal.Privileges {
			def.Permissions = append(def.Permissions, privilegeMap[privilege])
		}
		signalDefs = append(signalDefs, def)
		signalMap[signal.JSONName] = def
	}
	return nil
}
