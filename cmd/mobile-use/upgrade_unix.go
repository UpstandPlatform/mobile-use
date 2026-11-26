//go:build !windows

package main

import "os"

func installStagedBinary(staged, target string) (bool, error) {
	if err := os.Rename(staged, target); err != nil {
		return false, err
	}
	return false, nil
}
