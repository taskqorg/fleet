# Sourced by the deploy scripts. OSC 7501 program status (https://www.superlogical.com/rex/docs/build/program-status):
#   ESC ] 7501 ; state=<working|done|error>:app=fleet-deploy[:progress=<0-100>]:msg=<base64 UTF-8> ESC \
# Printed only when stdout is a terminal, so logs and pipes stay clean. Every step also prints a plain line on stderr.
status() { # status <working|done|error> <percent or empty> <text>
  if [ -t 1 ]; then
    progress=; [ "$1" = working ] && [ -n "$2" ] && progress=":progress=$2"
    printf '\033]7501;state=%s:app=fleet-deploy%s:msg=%s\033\\' "$1" "$progress" "$(printf %s "$3" | base64 | tr -d '\n')"
  fi
  echo "$3" >&2
}
trap 'code=$?; [ "$code" -eq 0 ] || status error "" "failed (exit $code)"' EXIT
