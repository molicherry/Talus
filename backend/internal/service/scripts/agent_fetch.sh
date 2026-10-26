# Variables are shell-quoted by the Hub; the private curl config contains the
# credential. Keep partial bytes on failure, but never keep that credential.
umask 077
trap 'rm -f -- "$fetch_config"' EXIT
trap 'exit 1' HUP INT TERM
if command -v sha256sum >/dev/null; then hash_tool=sha256sum
elif command -v shasum >/dev/null; then hash_tool=shasum
else
  printf '%s\n' TALUS_HASH_TOOL_MISSING
  exit 78
fi
test ! -L "$cache_dir" && mkdir -p -- "$cache_dir" && chmod 0700 "$cache_dir"
cache="$cache_dir/$digest.bin"
part="$cache_dir/$digest.part"
lock="$cache_dir/.lock"
for file in "$cache" "$part" "$lock" "$next"; do
  test ! -L "$file" || exit 1
  if test -e "$file"; then test -f "$file" || exit 1; fi
done
exec 9<>"$lock"
flock -x -w 10 9 || exit 1
# At most one complete artifact and one partial artifact per installation.
for file in "$cache_dir"/*; do
  test -e "$file" || continue
  case "$file" in "$cache"|"$part") ;; *) rm -f -- "$file" ;; esac
done
hash_file() {
  if test "$hash_tool" = sha256sum; then sum=$(sha256sum < "$1") || return 1
  else sum=$(shasum -a 256 < "$1") || return 1; fi
  printf '%s' "${sum%% *}"
}
valid_artifact() {
  test -f "$1" && test ! -L "$1" &&
    test "$(wc -c < "$1" | tr -d ' ')" = "$size" &&
    test "$(hash_file "$1")" = "$digest"
}
publish() {
  if ! valid_artifact "$part"; then rm -f -- "$part"; exit 77; fi
  mv -f -- "$part" "$cache"
  cp -- "$cache" "$next"
  chmod 0600 "$next"
  printf '%s\n' TALUS_ARTIFACT_READY
}
if test "$mode" = store; then
  valid_artifact "$next" || exit 77
  cp -- "$next" "$part"
  publish
  exit 0
fi
if valid_artifact "$cache"; then
  cp -- "$cache" "$next"
  chmod 0600 "$next"
  printf '%s\n' TALUS_ARTIFACT_READY
  exit 0
fi
if valid_artifact "$current"; then
  cp -- "$current" "$part"
  publish
  exit 0
fi
if valid_artifact "$part"; then publish; exit 0; fi
rm -f -- "$cache"
if test -f "$part" && test "$(wc -c < "$part")" -ge "$size"; then rm -f -- "$part"; fi
if ! command -v curl >/dev/null; then
  printf '%s\n' TALUS_PULL_UNAVAILABLE
  exit 75
fi
offset=0
if test -f "$part"; then offset=$(wc -c < "$part"); fi
pull() {
  status=0
  http_code=$(curl --config "$fetch_config" --fail --silent --show-error \
    --connect-timeout 10 --speed-time 30 --speed-limit 1 --max-time "$seconds" \
    --max-filesize "$size" --output "$part" --write-out '%{http_code}' "$@") || status=$?
}
pull --continue-at -
# A proxy that ignores Range must never have a full response appended to a part.
if test "$http_code" = 416 || { test "$http_code" = 200 && test "$status" = 33; }; then
  rm -f -- "$part"
  offset=0
  pull
fi
# Capacity contention is retried without starting another full SFTP transfer.
# curl --fail leaves existing partial bytes untouched on 429/503 responses.
case "$http_code" in
  429|503) printf '%s\n' TALUS_PULL_BUSY; exit 79 ;;
  200|206) ;;
  *)
    # Redirect/error bodies are not artifact progress. A resumed curl refuses
    # to append these responses; a new download may have saved their body.
    if test "$offset" = 0; then rm -f -- "$part"; fi
    printf '%s\n' TALUS_PULL_UNAVAILABLE
    exit 75
    ;;
esac
if test "$offset" = 0 && test "$http_code" != 200; then
  rm -f -- "$part"
  printf '%s\n' TALUS_PULL_UNAVAILABLE
  exit 75
fi
if test "$status" != 0; then
  if test -f "$part" && test "$(wc -c < "$part")" -gt "$offset"; then
    printf '%s\n' TALUS_PULL_INCOMPLETE
    exit 76
  fi
  printf '%s\n' TALUS_PULL_UNAVAILABLE
  exit 75
fi
if ! valid_artifact "$part"; then rm -f -- "$part"; exit 77; fi
publish
