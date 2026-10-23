//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the Live Calendar client only runs on Windows")
	os.Exit(1)
}
