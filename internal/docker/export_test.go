package docker

import "testing"

func TestCheckSaveAPIVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		api     string
		wantErr bool
	}{
		{api: "1.43", wantErr: true},
		{api: "1.41", wantErr: true},
		{api: "1.44"},
		{api: "1.52"},
	}
	for _, tt := range tests {
		t.Run(tt.api, func(t *testing.T) {
			t.Parallel()
			if err := checkSaveAPIVersion("x", tt.api); (err != nil) != tt.wantErr {
				t.Fatalf("checkSaveAPIVersion(%q) = %v, want error %v", tt.api, err, tt.wantErr)
			}
		})
	}
}
