package signals

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBareSignalName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips vss prefix", input: "vss.speed", expected: "speed"},
		{name: "strips prefix from nested name", input: "vss.currentLocationCoordinates", expected: "currentLocationCoordinates"},
		{name: "returns unchanged when no prefix", input: "speed", expected: "speed"},
		{name: "returns empty for empty string", input: "", expected: ""},
		{name: "does not strip partial prefix", input: "vs.speed", expected: "vs.speed"},
		{name: "handles prefix-only input", input: "vss.", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BareSignalName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultPermissions(t *testing.T) {
	assert.Equal(t, []string{
		"privilege:GetNonLocationHistory",
		"privilege:GetLocationHistory",
	}, DefaultPermissions)
}

func TestDefaultDefinition(t *testing.T) {
	def := DefaultDefinition("speed", NumberType)
	assert.Equal(t, "speed", def.Name)
	assert.Equal(t, NumberType, def.ValueType)
	assert.Equal(t, DefaultPermissions, def.Permissions)
}

func TestGetSignalDefinitionOrDefault_UnknownSignal(t *testing.T) {
	def := GetSignalDefinitionOrDefault("nonExistentSignal", StringType)
	assert.Equal(t, "nonExistentSignal", def.Name)
	assert.Equal(t, StringType, def.ValueType)
	assert.Equal(t, DefaultPermissions, def.Permissions)
}
