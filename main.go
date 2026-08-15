package main

import (
	"fmt"
	"os"

	"polaris/cmd"
)

func main() {
	cmd.AppVersion = Version
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
