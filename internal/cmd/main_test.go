package cmd

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Unit tests never talk to a cluster: no ImagePatch objects unless a test says otherwise
	imagePatchLister = func() ([]string, error) { return nil, nil }
	os.Exit(m.Run())
}
