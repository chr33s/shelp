package shells

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellWidgetsAndCompleters(t *testing.T) {
	tests := []struct{ name, source, script string }{
		{"bash", Bash, `
shelp() { printf 'git\ngithub\n'; }
READLINE_LINE='gi --version'; READLINE_POINT=2
_shelp_widget
printf 'first=%s:%s\n' "$READLINE_LINE" "$READLINE_POINT"
_shelp_widget
printf 'second=%s:%s\n' "$READLINE_LINE" "$READLINE_POINT"
READLINE_POINT=2
shelp() { [[ "${@: -1}" == gi ]] || return 1; printf 'gist\n'; }
_shelp_widget
printf 'moved=%s:%s\n' "$READLINE_LINE" "$READLINE_POINT"
shelp() { printf 'git\ngithub\n'; }
COMP_WORDS=(shelp gi); COMP_CWORD=1
_shelp_complete
printf 'candidates=%s\n' "${COMPREPLY[*]}"
`},
		{"zsh", Zsh, `
shelp() { printf 'git\ngithub\n'; }
BUFFER='gi --version'; CURSOR=2
_shelp_widget
printf 'first=%s:%s\n' "$BUFFER" "$CURSOR"
_shelp_widget
printf 'second=%s:%s\n' "$BUFFER" "$CURSOR"
CURSOR=2
shelp() { [[ "${@[-1]}" == gi ]] || return 1; printf 'gist\n'; }
_shelp_widget
printf 'moved=%s:%s\n' "$BUFFER" "$CURSOR"
shelp() { printf 'git\ngithub\n'; }
compadd() { shift 3; printf 'candidates=%s\n' "$*"; }
words=(shelp gi); CURRENT=2
_shelp_complete
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := exec.LookPath(tt.name)
			if err != nil {
				t.Skip(tt.name + " unavailable")
			}
			out, err := exec.Command(path, "-c", tt.source+tt.script).CombinedOutput()
			if err != nil {
				t.Fatal(string(out), err)
			}
			for _, want := range []string{"first=git --version:3", "second=github --version:6", "candidates=git github", "moved=gistthub --version:4"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("missing %q in %s", want, out)
				}
			}
		})
	}
}
