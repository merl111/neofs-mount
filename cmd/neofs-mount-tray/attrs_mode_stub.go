//go:build !windows

package main

func tryNeoFSAttrsMode() (done bool, exitCode int) {
	return false, 0
}
