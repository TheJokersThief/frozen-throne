package engine

import "testing"

func TestStubReturnsNotImplemented(t *testing.T) {
	if _, err := New().Evaluate(Input{}); err != ErrNotImplemented {
		t.Fatalf("got %v", err)
	}
}
