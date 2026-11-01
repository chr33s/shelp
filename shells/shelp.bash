# Evaluate with: eval "$(shelp init bash)"
# Ctrl-X Ctrl-H cycles suggestions without executing them.
_shelp_candidates=()
_shelp_last=''
_shelp_suffix=''
_shelp_choice=0
_shelp_cursor=-1
_shelp_widget() {
  local output candidate
  if [[ "$READLINE_LINE" != "$_shelp_last" || $READLINE_POINT -ne $_shelp_cursor || ${#_shelp_candidates[@]} -eq 0 ]]; then
    output=$(shelp complete --shell bash -- "${READLINE_LINE:0:READLINE_POINT}") || return
    [[ -n "$output" ]] || return
    _shelp_suffix=${READLINE_LINE:READLINE_POINT}
    _shelp_candidates=()
    while IFS= read -r candidate; do _shelp_candidates+=("$candidate"); done <<< "$output"
    _shelp_choice=0
  fi
  READLINE_LINE=${_shelp_candidates[_shelp_choice]}$_shelp_suffix
  READLINE_POINT=${#_shelp_candidates[_shelp_choice]}
  _shelp_last=$READLINE_LINE
  _shelp_cursor=$READLINE_POINT
  _shelp_choice=$(( (_shelp_choice + 1) % ${#_shelp_candidates[@]} ))
}
_shelp_complete() {
  local candidate
  COMPREPLY=()
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] && COMPREPLY+=("$candidate")
  done < <(shelp complete --shell bash -- "${COMP_WORDS[COMP_CWORD]}")
}
if [[ $- == *i* ]] && (( BASH_VERSINFO[0] >= 4 )); then
  bind -x '"\C-x\C-h":_shelp_widget'
  complete -F _shelp_complete -E
  # Bash 5 adds programmable completion of the initial command word.
  if (( BASH_VERSINFO[0] >= 5 )); then complete -F _shelp_complete -I; fi
fi
