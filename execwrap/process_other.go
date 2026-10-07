//go:build !unix

package execwrap

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Never start an uncontained probe on a platform without tree cleanup.
func prepareProcessCleanup(_ *exec.Cmd) (func(), error) {
	return nil, fmt.Errorf("process execution is unsupported on %s", runtime.GOOS)
}
