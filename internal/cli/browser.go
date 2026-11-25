// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"errors"
	"net/url"
	"os/exec"
	"runtime"
)

// openBrowser opens an http or https page in the default browser, with the operating system's own
// opener. It is only called after the person pressed Enter to ask for it.
func openBrowser(page string) error {
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("not a web address")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u.String())
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	default:
		cmd = exec.Command("xdg-open", u.String())
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
