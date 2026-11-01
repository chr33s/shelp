// Package process runs subprocesses with bounded output and cancellation.
package process

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
	"time"
)

type buffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data.Write(p)
	}
	return n, nil
}

// Run drains output even after the limit and kills descendants on cancellation.
func Run(ctx context.Context, limit int, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, stderr := &buffer{limit: limit}, &buffer{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 200 * time.Millisecond
	configure(cmd)
	err := cmd.Run()
	cleanup(cmd)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return stdout.data.String(), stderr.data.String(), err
}
