package spawn

import (
	"syscall"
)

const (
	socketFD    = 3
	maxMessage  = 192 << 10
	maxEvent    = 4 << 10
	maxFDs      = 16
	stdFiles    = 3
	spawnerEnv  = "LYS_WM_SPAWNER_FD"
	spawnerName = "lys-wm-spawner"
	spawnerExec = "/proc/self/exe"
)

type eventKind string

const (
	eventStarted eventKind = "started"
	eventRefused eventKind = "refused"
	eventExited  eventKind = "exited"
)

type request struct {
	ID          uint64         `json:"id"`
	Path        string         `json:"path"`
	Args        []string       `json:"args"`
	Env         []string       `json:"env"`
	Setsid      bool           `json:"setsid,omitempty"`
	DeathSignal syscall.Signal `json:"death_signal,omitempty"`
}

type event struct {
	ID     uint64             `json:"id"`
	Kind   eventKind          `json:"kind"`
	PID    int                `json:"pid,omitempty"`
	Status syscall.WaitStatus `json:"status,omitempty"`
	Err    string             `json:"err,omitempty"`
}
