package celcondition

import (
	"testing"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/stretchr/testify/require"
)

func TestPrepareCondition(t *testing.T) {
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
			name:        "simple numeric condition",
			condition:   "valueNumber > 10.0",
			expectError: false,
		},
		{
			name:        "simple string condition",
			condition:   "valueString == 'active'",
			expectError: false,
		},
		{
			name:        "complex condition with multiple variables",
			condition:   "valueNumber > 20.0 && valueString != 'off'",
			expectError: false,
		},

		{
			name:        "invalid CEL syntax",
			condition:   "valueNumber > >",
			expectError: true,
		},
		{
			name:        "undefined variable",
			condition:   "unknownVar == 5",
			expectError: true,
		},
		{
			name:        "type mismatch",
			condition:   "valueNumber == 'string'",
			expectError: true,
		},
		{
			name:        "integer comparison",
			condition:   "valueNumber > 10",
			expectError: false,
		},
		{
			name:        "numeric operations non zero",
			condition:   "valueNumber + 10.0",
			expectError: true,
		},
		{
			name:        "numeric operations zero",
			condition:   "valueNumber",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrepareCondition(tt.condition)

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
		name        string
		condition   string
		signal      *vss.Signal
		expected    bool
		expectError bool
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// First prepare the condition
			prg, err := PrepareCondition(tt.condition)
			require.NoError(t, err)
			require.NotNil(t, prg)

			// Then evaluate it
			result, err := EvaluateCondition(prg, tt.signal)
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
	prg, err := PrepareCondition("valueNumber > 10.0")
	if err != nil {
		t.Fatalf("failed to prepare condition: %v", err)
	}
	if prg == nil {
		t.Fatalf("expected non-nil program")
	}

	// This should handle nil signal gracefully
	_, err = EvaluateCondition(prg, nil)
	if err == nil {
		t.Error("expected error when evaluating with nil signal, got nil")
	}
}

func TestEndToEndScenarios(t *testing.T) {
	scenarios := []struct {
		name        string
		condition   string
		signals     []*vss.Signal
		expected    []bool
		description string
	}{
		{
			name:      "speed threshold monitoring",
			condition: "valueNumber > 65.0",
			signals: []*vss.Signal{
				{ValueNumber: 60.0, ValueString: ""},
				{ValueNumber: 70.0, ValueString: ""},
				{ValueNumber: 65.0, ValueString: ""},
				{ValueNumber: 66.0, ValueString: ""},
			},
			expected:    []bool{false, true, false, true},
			description: "Monitor when vehicle exceeds speed limit",
		},
		{
			name:      "engine status monitoring",
			condition: "valueString == 'running' && valueNumber > 800.0",
			signals: []*vss.Signal{
				{ValueNumber: 750.0, ValueString: "running"},
				{ValueNumber: 850.0, ValueString: "running"},
				{ValueNumber: 900.0, ValueString: "idle"},
				{ValueNumber: 820.0, ValueString: "running"},
			},
			expected:    []bool{false, true, false, true},
			description: "Monitor engine running at high RPM",
		},
		{
			name:      "fuel level warning",
			condition: "valueNumber <= 10.0 && valueString != 'charging'",
			signals: []*vss.Signal{
				{ValueNumber: 15.0, ValueString: "normal"},
				{ValueNumber: 8.0, ValueString: "normal"},
				{ValueNumber: 5.0, ValueString: "charging"},
				{ValueNumber: 7.0, ValueString: "low"},
			},
			expected:    []bool{false, true, false, true},
			description: "Warn when fuel is low but not charging",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Logf("Testing scenario: %s", scenario.description)

			// Prepare the condition once
			prg, err := PrepareCondition(scenario.condition)
			if err != nil {
				t.Fatalf("failed to prepare condition %q: %v", scenario.condition, err)
			}
			if prg == nil {
				t.Fatalf("expected non-nil program for condition %q", scenario.condition)
			}

			// Evaluate against each signal
			for i, signal := range scenario.signals {
				result, err := EvaluateCondition(prg, signal)
				if err != nil {
					t.Fatalf("failed to evaluate condition %q with signal %d: %v", scenario.condition, i, err)
				}
				if result != scenario.expected[i] {
					t.Errorf("Signal %d: valueNumber=%f, valueString=%s should return %t, got %t",
						i, signal.ValueNumber, signal.ValueString, scenario.expected[i], result)
				}
			}
		})
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
			prg, err := PrepareCondition(tt.condition)
			if err != nil {
				t.Fatalf("failed to prepare condition %q: %v", tt.condition, err)
			}
			if prg == nil {
				t.Fatalf("expected non-nil program for condition %q", tt.condition)
			}

			result, err := EvaluateCondition(prg, tt.signal)
			if err != nil {
				t.Errorf("unexpected error evaluating condition %q: %v", tt.condition, err)
			}
			if result != tt.expected {
				t.Errorf("expected %t for condition %q with signal %+v, got %t", tt.expected, tt.condition, tt.signal, result)
			}
		})
	}
}
