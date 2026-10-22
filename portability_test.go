package satlink_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// thirtyTwoBitTargets are 32-bit platforms the whole module, tests
// included, must compile for: gomobile builds android/arm (armeabi-v7a),
// and int has 32 bits on both, so a constant that does not fit in one, such
// as math.MaxInt32 + 1, would stop a package's tests from building there.
var thirtyTwoBitTargets = []string{"linux/386", "linux/arm"}

// otherSystemTargets are the other systems the module builds for, each with
// files of its own: the CLI's terminals and interruptions on FreeBSD,
// OpenBSD and Windows, serial's close-on-exec on the BSDs, and Android,
// whose 64-bit ABI builds without cgo.
var otherSystemTargets = []string{"freebsd/amd64", "openbsd/amd64", "windows/amd64", "android/arm64"}

// TestEveryPackageCompilesForThirtyTwoBitPlatforms type-checks every
// package of the module and its tests for each of thirtyTwoBitTargets,
// without cgo, as a cross-compiling build does. Running the tests there
// needs such a machine.
func TestEveryPackageCompilesForThirtyTwoBitPlatforms(t *testing.T) {
	vetEveryPackageFor(t, thirtyTwoBitTargets)
}

// TestEveryPackageCompilesForTheOtherSystems type-checks every package of
// the module and its tests for each of otherSystemTargets, without cgo, so
// that a file only another system compiles cannot break unseen.
func TestEveryPackageCompilesForTheOtherSystems(t *testing.T) {
	vetEveryPackageFor(t, otherSystemTargets)
}

// vetEveryPackageFor runs go vet on the whole module for each of targets,
// "GOOS/GOARCH" pairs, without cgo. It is skipped under -short.
func vetEveryPackageFor(t *testing.T, targets []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("type-checks the whole module for other platforms")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not on PATH")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			goos, goarch, _ := strings.Cut(target, "/")
			vet := exec.Command(goTool, "vet", "./...")
			vet.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")

			if output, err := vet.CombinedOutput(); err != nil {
				t.Errorf("go vet for %s: %v\n%s", target, err, output)
			}
		})
	}
}
