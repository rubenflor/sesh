//go:build !unix

package execwrap

import "os/exec"

func prepareProcessCleanup(command *exec.Cmd) func() { return func() {} }
