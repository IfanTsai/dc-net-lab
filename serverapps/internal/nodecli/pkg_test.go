package nodecli

import "testing"

func TestSplitTarget(t *testing.T) {
	tests := []struct {
		target, name, version string
		wantErr               bool
	}{
		{target: "demo", name: "demo"},
		{target: "demo@1.0.0", name: "demo", version: "1.0.0"},
		{target: "", wantErr: true},
		{target: "@1.0.0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			name, version, err := splitTarget(tt.target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}

			if name != tt.name || version != tt.version {
				t.Errorf("got %s@%s, want %s@%s", name, version, tt.name, tt.version)
			}
		})
	}
}
