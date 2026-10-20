package httpapi

import "os"

func pid() int { return os.Getpid() }
