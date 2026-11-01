package shells

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Exercise real compadd matching in ZLE; mocking compadd cannot detect its
// default rejection of candidates that do not begin with the typed prefix.
func TestZshNonPrefixCompletion(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 PTY harness unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", `
import os, pty, select, subprocess, sys, tempfile, time
master, slave = pty.openpty()
with tempfile.NamedTemporaryFile(mode='w', suffix='.zsh') as source:
    source.write(os.environ['SHELP_TEST_SOURCE'])
    source.flush()
    env = dict(os.environ, TERM='xterm')
    child = subprocess.Popen([sys.argv[1], '-f'], stdin=slave, stdout=slave, stderr=slave, env=env)
    os.close(slave)
    pending = bytearray()
    def read_until(marker):
        deadline = time.monotonic() + 5
        while marker not in pending:
            if time.monotonic() >= deadline:
                raise AssertionError('Missing %r in %r' % (marker, pending))
            if select.select([master], [], [], 0.1)[0]:
                pending.extend(os.read(master, 65536))
        end = pending.index(marker) + len(marker)
        result = bytes(pending[:end])
        del pending[:end]
        return result
    try:
        setup = ('source ' + source.name + '; '
                 'shelp() { case "${@[-1]}" in gti) print git;; storage) print du;; esac; }; '
                 'zle -C regression-complete complete-word _shelp_complete; '
                 "bindkey '^I' regression-complete; "
                 '_capture() { print -r -- "RESULT:${BUFFER}:END"; BUFFER=""; zle reset-prompt; }; '
                 "zle -N _capture; bindkey '^X' _capture; print SETUP_DONE\n")
        os.write(master, setup.encode())
        read_until(b'\r\nSETUP_DONE\r\n')
        for query, candidate in [('gti', 'git'), ('storage', 'du')]:
            os.write(master, query.encode() + b'\t\x18')
            captured = read_until(b':END\r\n')
            # Completion may append a trailing space.
            assert ('RESULT:' + candidate + ':END').encode() in captured or ('RESULT:' + candidate + ' :END').encode() in captured, captured
    finally:
        child.terminate()
        try:
            child.wait(timeout=2)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()
        os.close(master)
`, zsh)
	cmd.Env = append(os.Environ(), "SHELP_TEST_SOURCE="+Zsh)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Zsh completion: %v\n%s", err, out)
	}
}
