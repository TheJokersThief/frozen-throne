//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
)

var stack *Stack

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	s, err := Start(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot start stack: %v\n", err)
		return 1
	}
	stack = s
	defer s.Close()
	code := m.Run()
	if code != 0 {
		s.DumpLogs(os.Stderr) // compose only
	}
	return code
}
