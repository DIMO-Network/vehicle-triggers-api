package services

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestEvaluateCondition(t *testing.T) {
	logger := zerolog.Nop()
	listener := &SignalListener{
		log: logger,
	}

	tests := []struct {
		name      string
		condition string
		signal    Signal
		want      bool
		wantErr   bool
	}{
		{
			name:      "Empty condition returns true",
			condition: "",
			signal: Signal{
				ValueNumber: 75,
				ValueString: "foo",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueNumber > 100.0 false",
			condition: "valueNumber > 100.0",
			signal: Signal{
				ValueNumber: 50,
				ValueString: "bar",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "valueNumber > 100.0 true",
			condition: "valueNumber > 100.0",
			signal: Signal{
				ValueNumber: 150,
				ValueString: "baz",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "valueString equals active returns true",
			condition: "valueString == 'active'",
			signal: Signal{
				ValueNumber: 80,
				ValueString: "active",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "tokenId equals 1 returns true",
			condition: "tokenId == 1",
			signal: Signal{
				ValueNumber: 80,
				ValueString: "active",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "tokenId equals 1 fails for different token",
			condition: "tokenId == 1",
			signal: Signal{
				ValueNumber: 80,
				ValueString: "active",
				TokenID:     2,
				Timestamp:   time.Now(),
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "Invalid condition returns error",
			condition: "invalid condition",
			signal: Signal{
				ValueNumber: 80,
				ValueString: "active",
				TokenID:     1,
				Timestamp:   time.Now(),
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listener.evaluateCondition(tc.condition, &tc.signal)
			if (err != nil) != tc.wantErr {
				t.Errorf("evaluateCondition() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("evaluateCondition() = %v, want %v", got, tc.want)
			}
		})
	}
}
