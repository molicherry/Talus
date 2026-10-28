package service

import _ "embed"

//go:embed scripts/flock_wait.sh
var residentFlockWait string
