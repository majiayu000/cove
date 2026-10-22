package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := strings.TrimSuffix(filepath.Base(executable), ".exe")
	version, err := os.ReadFile(filepath.Join(filepath.Dir(executable), ".cove-fixture-"+name+"-version"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(version))
}
