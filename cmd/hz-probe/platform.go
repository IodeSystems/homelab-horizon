package main

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
)

// platformKey is the "<os>-<arch>" key hz serves binaries under.
func platformKey() (string, error) {
	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64":
	case "arm":
	default:
		return "", fmt.Errorf("no prebuilt agent for %s/%s", runtime.GOOS, arch)
	}
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("no prebuilt agent for %s", runtime.GOOS)
	}
	return runtime.GOOS + "-" + arch, nil
}

// readJSON decodes a bounded response body.
func readJSON(r io.Reader, v any) error {
	return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(v)
}
