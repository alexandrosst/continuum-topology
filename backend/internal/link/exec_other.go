//go:build !linux

package link

import "os/exec"

// killGroupOnCancel has no process groups to use here: the context kills the program only (exec's default).
func killGroupOnCancel(*exec.Cmd) {}
