package vulkan

import (
	"testing"
	"time"
)

func TestAReadWithNothingToWaitForDoesNotBlock(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- (&pendingRead{fence: -1}).Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked on a read with no fence")
	}
}
