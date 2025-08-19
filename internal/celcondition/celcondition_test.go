package celcondition

import (
	"testing"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/stretchr/testify/require"
)

func TestPrepareCondition(t *testing.T) {
	tests := []struct {
		name        string
		condition   string
		valueType   string
		expectError bool
	}{
		{
			name:        "empty condition",
			condition:   "",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "simple numeric condition",
			condition:   "valueNumber > 10.0",
			expectError: false,
			valueType:   signals.NumberType,
		},
		{
			name:        "simple string condition",
			condition:   "valueString == 'active'",
			expectError: false,
			valueType:   signals.StringType,
		},

		{
			name:        "invalid CEL syntax",
			condition:   "valueNumber > >",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "undefined variable",
			condition:   "unknownVar == 5",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "type mismatch",
			condition:   "valueNumber >= 'string'",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "type mismatch equality",
			condition:   "valueNumber == 'string'",
			expectError: false,
			valueType:   signals.NumberType,
		},
		{
			name:        "integer comparison",
			condition:   "valueNumber >= 1",
			expectError: false,
			valueType:   signals.NumberType,
		}, {
			name:        "integer comparison",
			condition:   "valueNumber == 1",
			expectError: false,
			valueType:   signals.NumberType,
		},
		{
			name:        "numeric operations non zero",
			condition:   "valueNumber + 10.0",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "numeric operations zero",
			condition:   "valueNumber",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "generic value as number",
			condition:   "value > 10.0",
			expectError: false,
			valueType:   signals.NumberType,
		},
		{
			name:        "generic value as string",
			condition:   "value == 'active'",
			expectError: false,
			valueType:   signals.StringType,
		},
		{
			name:        "generic value as number used as string",
			condition:   "value < 'active'",
			expectError: true,
			valueType:   signals.NumberType,
		},
		{
			name:        "generic value as string used as number",
			condition:   "value > 10.0",
			expectError: true,
			valueType:   signals.StringType,
		},
		{
			name:        "High cost condition ok",
			condition:   "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90].all(x, x != -1)",
			expectError: false,
			valueType:   signals.NumberType,
		},
		{
			name:        "High cost condition fail",
			condition:   "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90].all(x - 99, x != -1)",
			expectError: true,
			valueType:   signals.NumberType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareSignalCondition(tt.condition, tt.valueType)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		name           string
		condition      string
		signal         *vss.Signal
		previousSignal *vss.Signal
		expected       bool
		expectError    bool
	}{
		{
			name:      "numeric condition true",
			condition: "valueNumber > 20.0",
			signal: &vss.Signal{
				ValueNumber: 25.0,
				ValueString: "",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "numeric condition false",
			condition: "valueNumber > 20.0",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "string condition true",
			condition: "valueString == 'active'",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "active",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "string condition false",
			condition: "valueString == 'active'",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "inactive",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "complex condition true",
			condition: "valueNumber >= 10.0 && valueString != 'off'",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "on",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "complex condition false - number fails",
			condition: "valueNumber >= 10.0 && valueString != 'off'",
			signal: &vss.Signal{
				ValueNumber: 5.0,
				ValueString: "on",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "complex condition false - string fails",
			condition: "valueNumber >= 10.0 && valueString != 'off'",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "off",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "equality check with zero",
			condition: "valueNumber == 0.0",
			signal: &vss.Signal{
				ValueNumber: 0.0,
				ValueString: "",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "string contains check",
			condition: "valueString.contains('test')",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "this is a test string",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "numeric range check",
			condition: "valueNumber >= 10.0 && valueNumber <= 50.0",
			signal: &vss.Signal{
				ValueNumber: 25.0,
				ValueString: "",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "empty string check",
			condition: "valueString == ''",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "simple bool true",
			condition: "true",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "",
			},
			expected: true,
		},
		{
			name:      "simple bool false",
			condition: "false",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "",
			},
			expected: false,
		},
		{
			name:      "referenced previous signal true",
			condition: "previousValueNumber != valueNumber",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			previousSignal: &vss.Signal{
				ValueNumber: 10.0,
				ValueString: "",
			},
			expected: true,
		},
		{
			name:      "referenced previous signal false",
			condition: "previousValueNumber != valueNumber",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			previousSignal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			expected: false,
		},
		{
			name:      "referenced but missing previous signal",
			condition: "previousValueNumber != valueNumber",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			previousSignal: nil,
			expected:       true,
		},
		{
			name:      "generic value as number",
			condition: "value > 10.0",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "generic value as string",
			condition: "value == 'active'",
			signal: &vss.Signal{
				ValueNumber: 0,
				ValueString: "active",
			},
			expected:    true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First prepare the condition
			var valueType string
			if tt.signal.ValueString != "" {
				valueType = signals.StringType
			} else {
				valueType = signals.NumberType
			}
			prg, err := PrepareSignalCondition(tt.condition, valueType)
			require.NoError(t, err)
			require.NotNil(t, prg)

			// Then evaluate it
			result, err := EvaluateSignalCondition(prg, tt.signal, tt.previousSignal, valueType)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEvaluateCondition_WithNilSignal(t *testing.T) {
	prg, err := PrepareSignalCondition("valueNumber > 10.0", signals.NumberType)
	if err != nil {
		t.Fatalf("failed to prepare condition: %v", err)
	}
	if prg == nil {
		t.Fatalf("expected non-nil program")
	}

	// This should handle nil signal gracefully
	_, err = EvaluateSignalCondition(prg, nil, nil, signals.NumberType)
	if err == nil {
		t.Error("expected error when evaluating with nil signal, got nil")
	}
}

func TestPrepareCondition_WithDecimalNumbers(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		signal    *vss.Signal
		expected  bool
	}{
		{
			name:      "decimal comparison",
			condition: "valueNumber > 10.5",
			signal: &vss.Signal{
				ValueNumber: 15.0,
				ValueString: "",
			},
			expected: true,
		},
		{
			name:      "precise decimal comparison",
			condition: "valueNumber == 25.75",
			signal: &vss.Signal{
				ValueNumber: 25.75,
				ValueString: "",
			},
			expected: true,
		},
		{
			name:      "complex decimal condition",
			condition: "valueNumber >= 0.5 && valueNumber <= 99.9",
			signal: &vss.Signal{
				ValueNumber: 50.25,
				ValueString: "",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the full flow
			prg, err := PrepareSignalCondition(tt.condition, signals.NumberType)
			if err != nil {
				t.Fatalf("failed to prepare condition %q: %v", tt.condition, err)
			}
			if prg == nil {
				t.Fatalf("expected non-nil program for condition %q", tt.condition)
			}

			result, err := EvaluateSignalCondition(prg, tt.signal, tt.signal, signals.NumberType)
			if err != nil {
				t.Errorf("unexpected error evaluating condition %q: %v", tt.condition, err)
			}
			if result != tt.expected {
				t.Errorf("expected %t for condition %q with signal %+v, got %t", tt.expected, tt.condition, tt.signal, result)
			}
		})
	}
}

func TestPrepareCondition_ServiceName(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		condition   string
		expectError bool
	}{
		{
			name:        "telemetry.signals service",
			serviceName: triggersrepo.ServiceSignal,
			condition:   "valueNumber > 10.0",
			expectError: false,
		},
		{
			name:        "telemetry.events service",
			serviceName: triggersrepo.ServiceEvent,
			condition:   "name == 'HarshBraking'",
			expectError: false,
		},
		{
			name:        "unknown service",
			serviceName: "unknown.service",
			condition:   "valueNumber > 10.0",
			expectError: true,
		},
		{
			name:        "empty service name",
			serviceName: "",
			condition:   "valueNumber > 10.0",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareCondition(tt.serviceName, tt.condition, signals.NumberType)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPrepareEventCondition(t *testing.T) {
	tests := []struct {
		name        string
		condition   string
		expectError bool
	}{
		{
			name:        "empty condition",
			condition:   "",
			expectError: true,
		},
		{
			name:        "simple name condition",
			condition:   "name == 'HarshBraking'",
			expectError: false,
		},
		{
			name:        "simple source condition",
			condition:   "source == '0x1234567890abcdef1234567890abcdef12345678'",
			expectError: false,
		},
		{
			name:        "duration condition",
			condition:   "durationNs > 1000",
			expectError: false,
		},
		{
			name:        "duration equals",
			condition:   "durationNs == 1000",
			expectError: false,
		},
		{
			name:        "metadata condition",
			condition:   "metadata.contains('emergency')",
			expectError: false,
		},
		{
			name:        "complex condition with multiple variables",
			condition:   "name == 'HarshBraking' && source == '0x1234567890abcdef1234567890abcdef12345678' && durationNs > 500",
			expectError: false,
		},
		{
			name:        "previous event comparison",
			condition:   "name != previousName",
			expectError: false,
		},
		{
			name:        "duration comparison with previous",
			condition:   "durationNs > previousDurationNs",
			expectError: false,
		},
		{
			name:        "invalid CEL syntax",
			condition:   "name == ==",
			expectError: true,
		},
		{
			name:        "undefined variable",
			condition:   "unknownVar == 'test'",
			expectError: true,
		},
		{
			name:        "type mismatch",
			condition:   "durationNs >= 'string'",
			expectError: true,
		},
		{
			name:        "non-boolean return type",
			condition:   "name",
			expectError: true,
		},
		{
			name:        "simple boolean true",
			condition:   "true",
			expectError: false,
		},
		{
			name:        "simple boolean false",
			condition:   "false",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareEventCondition(tt.condition)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEvaluateEventCondition(t *testing.T) {
	tests := []struct {
		name          string
		condition     string
		event         *vss.Event
		previousEvent *vss.Event
		expected      bool
		expectError   bool
	}{
		{
			name:      "name condition true",
			condition: "name == 'HarshBraking'",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "HarshBraking",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "HarshCornering",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "name condition false",
			condition: "name == 'HarshBraking'",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "HarshCornering",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "HarshBraking",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "source condition true",
			condition: "source == '0x1234567890abcdef1234567890abcdef12345678'",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "HarshBraking",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
				Name:       "HarshBraking",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "duration condition true",
			condition: "durationNs > 500",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 300,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "duration condition false",
			condition: "durationNs > 500",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 300,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "metadata condition true",
			condition: "metadata.contains('emergency')",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "AlarmTriggered",
				DurationNs: 1000,
				Metadata:   "{\"type\": \"emergency\", \"level\": \"high\"}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "metadata condition false",
			condition: "metadata.contains('emergency')",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{\"type\": \"normal\"}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorClosed",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "complex condition true",
			condition: "name == 'DoorOpened' && source == '0x1234567890abcdef1234567890abcdef12345678' && durationNs > 500",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
				Name:       "DoorClosed",
				DurationNs: 300,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "complex condition false - name fails",
			condition: "name == 'DoorOpened' && source == '0x1234567890abcdef1234567890abcdef12345678' && durationNs > 500",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorClosed",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "previous event comparison true",
			condition: "name != previousName",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorClosed",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "previous event comparison false",
			condition: "name != previousName",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "duration comparison with previous true",
			condition: "durationNs > previousDurationNs",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "duration comparison with previous false",
			condition: "durationNs > previousDurationNs",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 300,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
		{
			name:      "simple bool true",
			condition: "true",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorClosed",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    true,
			expectError: false,
		},
		{
			name:      "simple bool false",
			condition: "false",
			event: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorOpened",
				DurationNs: 1000,
				Metadata:   "{}",
			},
			previousEvent: &vss.Event{
				Source:     "0x1234567890abcdef1234567890abcdef12345678",
				Name:       "DoorClosed",
				DurationNs: 500,
				Metadata:   "{}",
			},
			expected:    false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First prepare the condition
			prg, err := PrepareEventCondition(tt.condition)
			require.NoError(t, err)
			require.NotNil(t, prg)

			// Then evaluate it
			result, err := EvaluateEventCondition(prg, tt.event, tt.previousEvent)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEvaluateEventCondition_WithNilEvent(t *testing.T) {
	prg, err := PrepareEventCondition("name == 'DoorOpened'")
	require.NoError(t, err)
	require.NotNil(t, prg)

	// Test with nil current event
	previousEvent := &vss.Event{
		Source:     "0x1234567890abcdef1234567890abcdef12345678",
		Name:       "DoorClosed",
		DurationNs: 500,
		Metadata:   "{}",
	}

	// This should not panic but may fail depending on implementation
	_, err = EvaluateEventCondition(prg, nil, previousEvent)
	// We expect this to either work with empty/zero values or return an error
	// The function should handle nil gracefully
	require.Error(t, err)

	// Test with nil previous event
	currentEvent := &vss.Event{
		Source:     "0x1234567890abcdef1234567890abcdef12345678",
		Name:       "DoorOpened",
		DurationNs: 1000,
		Metadata:   "{}",
	}

	_, err = EvaluateEventCondition(prg, currentEvent, nil)
	require.NoError(t, err)
	// Test with both nil
	_, err = EvaluateEventCondition(prg, nil, nil)
	require.Error(t, err)
}
