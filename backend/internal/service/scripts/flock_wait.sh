# BusyBox flock supports nonblocking descriptor locks but may not provide -w.
# Keep the same open descriptor for every attempt: activation, rollback and
# cleanup must serialize on the original lock inode. Never continue unlocked.
talus_acquire_lock() {
  talus_lock_remaining=10
  while :; do
    talus_lock_status=0
    flock -n 9 || talus_lock_status=$?
    case "$talus_lock_status" in
      0) return 0 ;;
      1) ;;
      *) printf '%s\n' TALUS_LOCK_UNAVAILABLE >&2; return 1 ;;
    esac
    if test "$talus_lock_remaining" -eq 0; then
      printf '%s\n' TALUS_LOCK_TIMEOUT >&2
      return 1
    fi
    talus_lock_remaining=$((talus_lock_remaining - 1))
    sleep 1 || return 1
  done
}
