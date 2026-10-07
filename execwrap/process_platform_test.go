package execwrap

import (
	"go/build"
	"testing"
)

func TestProcessPlatformSelection(t *testing.T) {
	for _, platform := range []struct {
		goos, goarch, implementation string
	}{
		{"darwin", "arm64", "process_unix.go"},
		{"linux", "amd64", "process_unix.go"},
		{"windows", "amd64", "process_other.go"},
		{"windows", "arm64", "process_other.go"},
		{"plan9", "amd64", "process_other.go"},
	} {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH = platform.goos, platform.goarch
		for _, name := range []string{"process_unix.go", "process_other.go"} {
			matches, err := ctx.MatchFile(".", name)
			if err != nil {
				t.Fatal(err)
			}
			if matches != (name == platform.implementation) {
				t.Errorf("%s/%s selects %s = %v", platform.goos, platform.goarch, name, matches)
			}
		}
	}
}
