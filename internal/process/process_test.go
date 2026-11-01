//go:build unix

package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCancellationKillsOutputHoldingDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := Run(ctx, 100, "/bin/sh", "-c", "sleep 20 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancellation: %s %v", time.Since(start), err)
	}
}
func TestOutputLimitAndStderr(t *testing.T) {
	out, stderr, err := Run(context.Background(), 5, "/bin/sh", "-c", "printf 123456789; printf error123 >&2")
	if err != nil || out != "12345" || stderr != "error" {
		t.Fatal(out, stderr, err)
	}
	if strings.Contains(out, "error") {
		t.Fatal("stderr mixed with stdout")
	}
}

func TestExitedParentDoesNotLeavePipeHoldingChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := Run(ctx, 100, "/bin/sh", "-c", "sleep 20 & exit 0")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("inherited pipe: %s %v", time.Since(start), err)
	}
}
