//go:build !unix

package process

import "os/exec"

func configure(cmd *exec.Cmd) {}

func cleanup(cmd *exec.Cmd) {}
