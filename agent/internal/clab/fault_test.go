package clab

import (
	"reflect"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/runtime"
)

func TestNetemArgs(t *testing.T) {
	tests := []struct {
		name string
		imp  runtime.Impairment
		want []string
	}{
		{
			name: "delay only",
			imp:  runtime.Impairment{DelayMs: 100},
			want: []string{"delay", "100ms"},
		},
		{
			name: "delay with jitter",
			imp:  runtime.Impairment{DelayMs: 100, JitterMs: 10},
			want: []string{"delay", "100ms", "10ms"},
		},
		{
			name: "jitter without delay is dropped: netem has no bare jitter clause",
			imp:  runtime.Impairment{JitterMs: 10},
			want: nil,
		},
		{
			name: "loss only",
			imp:  runtime.Impairment{LossPercent: 1.5},
			want: []string{"loss", "1.5%"},
		},
		{
			name: "rate only",
			imp:  runtime.Impairment{RateKbit: 10000},
			want: []string{"rate", "10000kbit"},
		},
		{
			name: "all combined in tc's own delay/loss/rate order",
			imp:  runtime.Impairment{DelayMs: 100, JitterMs: 10, LossPercent: 1, RateKbit: 5000},
			want: []string{"delay", "100ms", "10ms", "loss", "1%", "rate", "5000kbit"},
		},
		{
			name: "nothing set",
			imp:  runtime.Impairment{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := netemArgs(tt.imp)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("netemArgs(%+v) = %v, want %v", tt.imp, got, tt.want)
			}
		})
	}
}
