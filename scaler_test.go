package scaler

import (
	"testing"
	"time"
)

func TestParseScalerConfig(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   Config
	}{
		{
			"defaults",
			map[string]string{},
			Config{Min: 1, Max: 10, CPUTarget: 70, RAMTarget: 80, Cooldown: 60 * time.Second},
		},
		{
			"custom",
			map[string]string{
				labelMin:      "2",
				labelMax:      "20",
				labelCPU:      "60",
				labelRAM:      "75",
				labelCooldown: "30s",
			},
			Config{Min: 2, Max: 20, CPUTarget: 60, RAMTarget: 75, Cooldown: 30 * time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseScalerConfig(tt.labels)
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
