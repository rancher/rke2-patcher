package kube_test

import (
	"testing"

	"github.com/rancher/rke2-patcher/internal/kube"
)

func TestScanImageWithTrivyJob(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		image   string
		want    []byte
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := kube.ScanImageWithTrivyJob(tt.image)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("ScanImageWithTrivyJob() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("ScanImageWithTrivyJob() succeeded unexpectedly")
			}
			// TODO: update the condition below to compare got with tt.want.
			if true {
				t.Errorf("ScanImageWithTrivyJob() = %v, want %v", got, tt.want)
			}
		})
	}
}
