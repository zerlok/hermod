package cli

import "syscall"

// ioctlGetTermios is the request that reads a terminal's attributes.
const ioctlGetTermios = syscall.TIOCGETA
