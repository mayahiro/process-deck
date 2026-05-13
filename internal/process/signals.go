package process

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func ParseSignal(value string) (os.Signal, error) {
	name := strings.ToUpper(strings.TrimSpace(value))
	name = strings.TrimPrefix(name, "SIG")
	if name == "" {
		return defaultStopSignal(), nil
	}

	switch name {
	case "TERM":
		return syscall.SIGTERM, nil
	case "INT":
		return syscall.SIGINT, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "QUIT":
		return syscall.SIGQUIT, nil
	default:
		return nil, fmt.Errorf("unknown signal %q", value)
	}
}

func defaultStopSignal() os.Signal {
	return syscall.SIGTERM
}
