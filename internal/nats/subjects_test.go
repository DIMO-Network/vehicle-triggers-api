package nats

import "testing"

func TestSignalSubject(t *testing.T) {
	cases := []struct {
		name     string
		signal   string
		expected string
	}{
		{"simple", "speed", "dimo.signals.speed"},
		{"dotted signal", "powertrain.fuelLevel", "dimo.signals.powertrain_fuelLevel"},
		{"wildcard stripped", "speed>", "dimo.signals.speed_"},
		{"empty signal becomes placeholder", "", "dimo.signals._"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SignalSubject(tc.signal); got != tc.expected {
				t.Fatalf("SignalSubject(%q) = %q, want %q", tc.signal, got, tc.expected)
			}
		})
	}
}

func TestEventSubject(t *testing.T) {
	if got := EventSubject("harshBraking"); got != "dimo.events.harshBraking" {
		t.Fatalf("got %q", got)
	}
}

func TestAuditSubject(t *testing.T) {
	if got := AuditSubject("0xDEADBEEF"); got != "dimo.trigger.fired.0xDEADBEEF" {
		t.Fatalf("got %q", got)
	}
}

func TestSignalFilter(t *testing.T) {
	if got := SignalFilter("speed"); got != "dimo.signals.speed" {
		t.Fatalf("got %q", got)
	}
}

func TestEventFilter(t *testing.T) {
	if got := EventFilter("ignitionOn"); got != "dimo.events.ignitionOn" {
		t.Fatalf("got %q", got)
	}
}

func TestAllSignalsFilter(t *testing.T) {
	if got := AllSignalsFilter(); got != "dimo.signals.>" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeReplacesIllegal(t *testing.T) {
	cases := map[string]string{
		"hello world":   "hello_world",
		"a.b.c":         "a_b_c",
		"wild*":         "wild_",
		"term>":         "term_",
		"tab\there":     "tab_here",
		"line\nbreak":   "line_break",
		"carriage\rret": "carriage_ret",
		"":              "_",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
