// Package shells embeds shell integration so go install produces one binary.
package shells

import _ "embed"

//go:embed shelp.zsh
var Zsh string

//go:embed shelp.bash
var Bash string
