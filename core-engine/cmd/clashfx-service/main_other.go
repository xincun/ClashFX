//go:build !windows && !darwin

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "clashfx-service only runs on Windows and macOS")
	os.Exit(1)
}
