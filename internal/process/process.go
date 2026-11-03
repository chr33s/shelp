// Package process runs subprocesses with bounded output and cancellation.
package process

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	return RunIn(ctx, "", limit, name, args...)
}

// RunIn is Run confined to dir when dir is non-empty: the working directory,
// TMPDIR, HOME and XDG base directories all point inside dir, so files and
// dotfiles a command creates land there instead of the caller's cwd or home.
// Stdin is always /dev/null.
func RunIn(ctx context.Context, dir string, limit int, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, stderr := &buffer{limit: limit}, &buffer{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return "", "", err
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	if dir != "" {
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TMPDIR="+dir, "HOME="+dir,
			"XDG_CONFIG_HOME="+filepath.Join(dir, ".config"),
			"XDG_CACHE_HOME="+filepath.Join(dir, ".cache"),
			"XDG_DATA_HOME="+filepath.Join(dir, ".local", "share"),
			"XDG_STATE_HOME="+filepath.Join(dir, ".local", "state"))
	}
	cmd.WaitDelay = 200 * time.Millisecond
	configure(cmd)
	err = cmd.Run()
	cleanup(cmd)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return stdout.data.String(), stderr.data.String(), err
}
