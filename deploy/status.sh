# Sourced by the deploy scripts. OSC 7501 program status for the terminal (Orca shows it as the tab's state):
#   ESC ] 7501 ; working ; <percent> ; <text> BEL     ESC ] 7501 ; done ; ; <text> BEL     ESC ] 7501 ; error ; ; <text> BEL
# Printed only when stdout is a terminal, so logs and pipes stay clean. Every step also prints a plain line on stderr.
status() { # status <working|done|error> <percent or empty> <text>
  [ -t 1 ] && printf '\033]7501;%s;%s;%s\007' "$1" "$2" "$3"
  echo "$3" >&2
}
trap 'code=$?; [ "$code" -eq 0 ] || status error "" "failed (exit $code)"' EXIT
