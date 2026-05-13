package celcondition

import (
	"sync"
	"testing"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/stretchr/testify/require"
)

// These tests guard the shared package-level cel.Env behaviour introduced to
// fix the startup OOM. Run with `go test -race` to catch concurrent mutation
// of internal checker state.

func TestPrepareCondition_ConcurrentCompile_NoRace(t *testing.T) {
	signalConditions := []struct {
		expr      string
		valueType string
	}{
		{`valueNumber > 10`, signals.NumberType},
		{`valueNumber < -5 || valueNumber > 30.5`, signals.NumberType},
		{`valueString == "park"`, signals.StringType},
		{`valueString != previousValueString`, signals.StringType},
		{`geoDistance(value.latitude, value.longitude, 37.7, -122.4) < 5.0`, signals.LocationType},
		{`value.hdop > 2.5`, signals.LocationType},
	}
	eventConditions := []string{
		`name == "ignition.on"`,
		`durationNs > 0`,
		`source != previousSource`,
		`metadata == "" || previousMetadata == ""`,
	}

	const goroutines = 64
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines*iterations)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (gid + i) % 3 {
				case 0, 1:
					tc := signalConditions[(gid+i)%len(signalConditions)]
					if _, err := PrepareSignalCondition(tc.expr, tc.valueType); err != nil {
						errCh <- err
					}
				case 2:
					expr := eventConditions[(gid+i)%len(eventConditions)]
					if _, err := PrepareEventCondition(expr); err != nil {
						errCh <- err
					}
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent compile failed: %v", err)
	}
}

func TestPrepareCondition_ConcurrentEval_NoRace(t *testing.T) {
	// Build one signal program and one event program, then hammer both with
	// concurrent Eval calls across goroutines. Programs are read-only at
	// eval time and must be safe for concurrent use because the Kafka
	// consumers fan out message processing.
	signalPrg, err := PrepareSignalCondition(`valueNumber > 10`, signals.NumberType)
	require.NoError(t, err)
	eventPrg, err := PrepareEventCondition(`name == "ignition.on"`)
	require.NoError(t, err)

	signalSamples := []*vss.Signal{
		{Data: vss.SignalData{ValueNumber: 5}},
		{Data: vss.SignalData{ValueNumber: 20}},
		{Data: vss.SignalData{ValueNumber: -1}},
	}
	eventSamples := []*vss.Event{
		{Data: vss.EventData{Name: "ignition.on"}},
		{Data: vss.EventData{Name: "ignition.off"}},
		{Data: vss.EventData{Name: "door.open"}},
	}

	const goroutines = 64
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines*iterations)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			prev := &vss.Signal{}
			prevEv := &vss.Event{}
			for i := 0; i < iterations; i++ {
				if (gid+i)%2 == 0 {
					sig := signalSamples[(gid+i)%len(signalSamples)]
					if _, err := EvaluateSignalCondition(signalPrg, sig, prev, signals.NumberType); err != nil {
						errCh <- err
					}
					prev = sig
				} else {
					ev := eventSamples[(gid+i)%len(eventSamples)]
					if _, err := EvaluateEventCondition(eventPrg, ev, prevEv); err != nil {
						errCh <- err
					}
					prevEv = ev
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent eval failed: %v", err)
	}
}

func TestPrepareCondition_ConcurrentMixed_NoRace(t *testing.T) {
	// Worst case: half the goroutines compile fresh programs while the
	// other half evaluate pre-compiled programs. This exercises Compile +
	// Check + Program + Eval against the shared env simultaneously.
	pre, err := PrepareSignalCondition(`valueNumber > 10`, signals.NumberType)
	require.NoError(t, err)

	const goroutines = 64
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines*iterations)

	exprs := []string{
		`valueNumber > 0`,
		`valueNumber + 1 > 5`,
		`valueString == "x"`,
		`geoDistance(value.latitude, value.longitude, 0, 0) < 100.0`,
	}
	valueTypes := []string{
		signals.NumberType,
		signals.NumberType,
		signals.StringType,
		signals.LocationType,
	}

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if gid%2 == 0 {
					idx := (gid + i) % len(exprs)
					if _, err := PrepareSignalCondition(exprs[idx], valueTypes[idx]); err != nil {
						errCh <- err
					}
					continue
				}
				sig := &vss.Signal{Data: vss.SignalData{ValueNumber: float64(i)}}
				if _, err := EvaluateSignalCondition(pre, sig, &vss.Signal{}, signals.NumberType); err != nil {
					errCh <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("mixed compile/eval failed: %v", err)
	}
}

// SignalPrepareIsServiceAgnostic asserts that the unexported package-level env
// is reused: every call returns the same pointer, so we are not paying the
// per-call NewEnv allocation that caused the OOM.
func TestSignalEnv_SharedSingleton(t *testing.T) {
	first, err := signalEnv()
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		again, err := signalEnv()
		require.NoError(t, err)
		require.Same(t, first, again, "signalEnv must return the same *cel.Env instance")
	}
	firstEv, err := eventEnvOnce()
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		again, err := eventEnvOnce()
		require.NoError(t, err)
		require.Same(t, firstEv, again, "eventEnvOnce must return the same *cel.Env instance")
	}
	require.NotSame(t, first, firstEv, "signal and event envs must be distinct")
}

// AssertTriggerRepoTypes keeps the test compile dependency on the helpers it
// indirectly relies on (signal type matching etc.). Without this Go would
// flag the import as unused if some of the cases above are removed.
var _ = triggersrepo.IsSignalService
