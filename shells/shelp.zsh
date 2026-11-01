# Evaluate with: eval "$(shelp init zsh)"
# Ctrl-X Ctrl-H cycles suggestions without executing them.
typeset -ga _shelp_candidates
typeset -g _shelp_last='' _shelp_suffix=''
typeset -gi _shelp_choice=0 _shelp_cursor=-1
_shelp_widget() {
  local output
  if [[ "$BUFFER" != "$_shelp_last" || $CURSOR -ne $_shelp_cursor || ${#_shelp_candidates} -eq 0 ]]; then
    output=$(shelp complete --shell zsh -- "${BUFFER[1,CURSOR]}") || return
    _shelp_suffix=${BUFFER[CURSOR+1,-1]}
    _shelp_candidates=("${(@f)output}")
    [[ -n "$output" ]] || return
    _shelp_choice=0
  fi
  (( _shelp_choice = _shelp_choice % ${#_shelp_candidates} + 1 ))
  BUFFER=${_shelp_candidates[_shelp_choice]}$_shelp_suffix
  CURSOR=${#_shelp_candidates[_shelp_choice]}
  _shelp_last=$BUFFER
  _shelp_cursor=$CURSOR
}
_shelp_complete() {
  local -a candidates
  candidates=("${(@f)$(shelp complete --shell zsh -- "${words[CURRENT]}")}")
  compadd -Q -U -- "${candidates[@]}"
}
if [[ -o interactive ]]; then
  zle -N _shelp_widget
  bindkey '^X^H' _shelp_widget
  (( $+functions[compdef] )) && compdef _shelp_complete -command-
fi
