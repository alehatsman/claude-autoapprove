package wrapper

import "testing"

func TestNewWithConfig(t *testing.T) {
	tests := []struct {
		name         string
		config       *Config
		wantCountdown int
		wantBuffer   int
	}{
		{
			name: "Default config",
			config: nil,
			wantCountdown: 3,
			wantBuffer: 10000,
		},
		{
			name: "Custom countdown",
			config: &Config{
				CountdownSeconds: 1,
				BufferSize: 10000,
			},
			wantCountdown: 1,
			wantBuffer: 10000,
		},
		{
			name: "Custom buffer size",
			config: &Config{
				CountdownSeconds: 3,
				BufferSize: 5000,
			},
			wantCountdown: 3,
			wantBuffer: 5000,
		},
		{
			name: "Invalid values get defaults",
			config: &Config{
				CountdownSeconds: 0,
				BufferSize: -100,
			},
			wantCountdown: 3,
			wantBuffer: 10000,
		},
		{
			name: "Large countdown allowed",
			config: &Config{
				CountdownSeconds: 60,
				BufferSize: 10000,
			},
			wantCountdown: 60,
			wantBuffer: 10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWithConfig(tt.config)

			if w.countdownSeconds != tt.wantCountdown {
				t.Errorf("countdownSeconds = %d, want %d", w.countdownSeconds, tt.wantCountdown)
			}

			if w.maxBuffer != tt.wantBuffer {
				t.Errorf("maxBuffer = %d, want %d", w.maxBuffer, tt.wantBuffer)
			}

			// Verify other fields are initialized
			if !w.autoApprove {
				t.Error("autoApprove should be true by default")
			}

			if w.recheckBuffer == nil {
				t.Error("recheckBuffer channel should be initialized")
			}
		})
	}
}

func TestNew(t *testing.T) {
	w := New()

	if w.countdownSeconds != 3 {
		t.Errorf("New() should use default countdown of 3, got %d", w.countdownSeconds)
	}

	if w.maxBuffer != 10000 {
		t.Errorf("New() should use default buffer of 10000, got %d", w.maxBuffer)
	}
}
