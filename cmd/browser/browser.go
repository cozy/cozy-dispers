// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Taken from https://tip.golang.org/src/cmd/internal/browser/browser.go?m=text

// Package browser provides utilities for interacting with users' browsers.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Commands returns a list of possible commands to use to open a url.
func Commands() [][]string {
	var cmds [][]string
	if exe := os.Getenv("BROWSER"); exe != "" {
		cmds = append(cmds, []string{exe})
	}
	switch runtime.GOOS {
	case "darwin":
		cmds = append(cmds, []string{"/usr/bin/open"})
	case "windows":
		cmds = append(cmds, []string{"cmd", "/c", "start"})
	default:
		cmds = append(cmds, []string{"xdg-open"})
	}
	cmds = append(cmds,
		[]string{"chrome"},
		[]string{"google-chrome"},
		[]string{"chromium"},
		[]string{"firefox"},
	)
	return cmds
}

// Open tries to open url in a browser and reports whether it succeeded.
func Open(url string) bool {
	for _, args := range Commands() {
		cmd := exec.Command(args[0], append(args[1:], url)...) // #nosec
		if cmd.Start() == nil {
			return true
		}
	}
	return false
}
