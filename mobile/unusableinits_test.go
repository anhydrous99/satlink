package mobile_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The Objective-C header as scripts/build-bindings.sh leaves it in the
// framework. gobind declares no argument-less init for the classes whose Go
// constructor takes arguments, for which it writes only an init: that
// takes them, nor for those that stand for a Go implementation of an
// interface, and the init they inherit from NSObject
// makes an object with no Go side, whose first use ends the app.
// scripts/mark-unusable-inits.sh marks that init, and new, unavailable, so
// that the compiler refuses them, as Rust's Swift bindings had no such
// initializer. These tests run the script on gobind's header, as
// build-bindings.sh runs it on the copies gomobile puts into the
// framework, and compile Objective-C against it with the macOS SDK's
// clang. Swift imports the mark as an unavailable init(), and so refuses
// SatlinkMobileIridiumHandset() alike.

// markingScript is scripts/mark-unusable-inits.sh, from this package's
// directory, where go test runs the tests.
var markingScript = filepath.Join("..", "scripts", "mark-unusable-inits.sh")

// unavailableInit is the first declaration the script adds to a class it
// marks.
const unavailableInit = "- (nonnull instancetype)init NS_UNAVAILABLE;"

// unusableInits are the classes whose init the script marks unavailable,
// in alphabetical order, as the package documentation names them
// (Objects). A type that gains a constructor taking arguments joins them,
// and the documentation should name it too.
var unusableInits = []string{
	objcPrefix + "MobileByteTransport",
	objcPrefix + "MobileIridiumHandset",
	objcPrefix + "MobileLinkListener",
	objcPrefix + "MobileLogSink",
	objcPrefix + "MobileTLSConnector",
}

// documentedConstruction makes objects the ways the package documentation
// gives Objective-C and Swift apps, which the marked header must still
// allow.
const documentedConstruction = `#import "SatlinkMobile.objc.h"

void construct(id<SatlinkMobileByteTransport> transport) {
	NSError *error = nil;
	SatlinkMobileIridiumHandset *handset = SatlinkMobileNewIridiumHandset(transport, &error);
	handset = [[SatlinkMobileIridiumHandset alloc] init:transport];
	SatlinkMobileTLSSettings *settings = [[SatlinkMobileTLSSettings alloc] init];
	SatlinkMobileTLSConnector *connector = SatlinkMobileNewTLSConnector(settings, &error);
	connector = [[SatlinkMobileTLSConnector alloc] init:settings];
	SatlinkMobileSatelliteLink *link = [[SatlinkMobileSatelliteLink alloc] init];
	(void)handset;
	(void)connector;
	(void)link;
}
`

// markedClass is a gobind class as the script leaves it.
const markedClass = `@interface SatlinkMobileTLSConnector : NSObject <goSeqRefInterface> {
}
@property(strong, readonly) _Nonnull id _ref;

- (nonnull instancetype)initWithRef:(_Nonnull id)ref;
` + unavailableInit + `
+ (nonnull instancetype)new NS_UNAVAILABLE;
- (nullable instancetype)init:(SatlinkMobileTLSSettings* _Nullable)settings;
@end
`

func TestTheObjectiveCHeaderRefusesObjectsWithNoGoSide(t *testing.T) {
	skipUnlessThorough(t)
	output := runGobind(t, buildGobind(t), macosTarget(t))
	header := filepath.Join(output, objcHeader)

	if messages, err := runMarkingScript(t, header); err != nil {
		t.Fatalf("mark-unusable-inits.sh: %v\n%s", err, messages)
	}

	if marked := classesWithInitUnavailable(readGenerated(t, output, objcHeader)); !slices.Equal(marked, unusableInits) {
		t.Errorf("init is unavailable in %q, want exactly %q", marked, unusableInits)
	}
	checker := newObjCChecker(t, filepath.Dir(header))
	if messages, err := checker.check(t, "documented.m", documentedConstruction); err != nil {
		t.Errorf("the documented construction does not compile: %v\n%s", err, messages)
	}
	unusable, statements := unusableConstruction()
	messages, err := checker.check(t, "unusable.m", unusable)
	if err == nil {
		t.Fatal("objects made with the init and new the classes inherit compile")
	}
	for line, statement := range statements {
		if !refusedAsUnavailable(messages, "unusable.m", line) {
			t.Errorf("%s is not refused as unavailable:\n%s", statement, messages)
		}
	}
}

func TestMarkingAHeaderAgainChangesNothing(t *testing.T) {
	t.Parallel()
	header := writeHeader(t, markedClass)

	if messages, err := runMarkingScript(t, header); err != nil {
		t.Fatalf("mark-unusable-inits.sh: %v\n%s", err, messages)
	}

	if marked := readHeader(t, header); marked != markedClass {
		t.Errorf("the header is now\n%s\nwant it unchanged", marked)
	}
}

func TestMarkingRefusesAHeaderOfAnotherShape(t *testing.T) {
	t.Parallel()
	// A gobind that wrote its header otherwise would leave classes unmarked.
	cases := map[string]string{
		"no gobind class":              "@interface SatlinkMobileTLSConnector : NSObject\n@end\n",
		"a class without initWithRef:": "@interface SatlinkMobileTLSConnector : NSObject <goSeqRefInterface> {\n}\n@end\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			header := writeHeader(t, source)

			if _, err := runMarkingScript(t, header); err == nil {
				t.Error("mark-unusable-inits.sh accepted the header")
			}
			if refused := readHeader(t, header); refused != source {
				t.Errorf("the refused header is now %q, want it unchanged", refused)
			}
		})
	}
}

// runMarkingScript runs scripts/mark-unusable-inits.sh on header, with
// bash, and returns what it said; without bash, it skips the test.
func runMarkingScript(t *testing.T, header string) (messages string, err error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	output, err := exec.Command(bash, markingScript, header).CombinedOutput()
	return string(output), err
}

// writeHeader writes source as a header of its own and returns its path.
func writeHeader(t *testing.T, source string) string {
	t.Helper()
	header := filepath.Join(t.TempDir(), objcPrefix+"Mobile.objc.h")
	if err := os.WriteFile(header, []byte(source), 0o644); err != nil {
		t.Fatalf("writing the header: %v", err)
	}
	return header
}

// readHeader returns the contents of the header at path.
func readHeader(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the header: %v", err)
	}
	return string(source)
}

// classesWithInitUnavailable returns the classes of an Objective-C header
// whose init is marked unavailable, in alphabetical order.
func classesWithInitUnavailable(header string) []string {
	var classes []string
	class := ""
	for line := range strings.Lines(header) {
		declaration := strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(declaration, "@interface "); found {
			class, _, _ = strings.Cut(rest, " ")
		}
		if declaration == unavailableInit {
			classes = append(classes, class)
		}
	}
	slices.Sort(classes)
	return classes
}

// unusableConstruction returns Objective-C source that makes an object of
// each class in unusableInits with init and with new, a statement a line,
// and the statements by line number.
func unusableConstruction() (source string, statements map[int]string) {
	lines := []string{`#import "SatlinkMobile.objc.h"`, "", "void construct(void) {"}
	statements = map[int]string{}
	for _, class := range unusableInits {
		for _, statement := range []string{"[[" + class + " alloc] init]", "[" + class + " new]"} {
			lines = append(lines, "\t(void)"+statement+";")
			statements[len(lines)] = statement
		}
	}
	lines = append(lines, "}", "")
	return strings.Join(lines, "\n"), statements
}

// refusedAsUnavailable reports whether clang's messages refuse line of
// file for using something unavailable.
func refusedAsUnavailable(messages, file string, line int) bool {
	location := fmt.Sprintf("%s:%d:", file, line)
	for message := range strings.Lines(messages) {
		if strings.Contains(message, location) && strings.Contains(message, "error: ") && strings.Contains(message, "is unavailable") {
			return true
		}
	}
	return false
}

// objcChecker type-checks Objective-C sources against a gobind header with
// the macOS SDK's clang.
type objcChecker struct {
	toolchain macOSToolchain
	// includeDirectory holds the header and those it includes.
	includeDirectory string
	// moduleCache is where clang keeps the modules it builds, Foundation's
	// among them, from one source to the next.
	moduleCache string
}

// newObjCChecker returns a checker for the header in includeDirectory,
// skipping the test without the macOS SDK.
func newObjCChecker(t *testing.T, includeDirectory string) objcChecker {
	t.Helper()
	toolchain, unavailable := findMacOSToolchain()
	if unavailable != "" {
		t.Skip(unavailable)
	}
	return objcChecker{toolchain: toolchain, includeDirectory: includeDirectory, moduleCache: t.TempDir()}
}

// check type-checks source as the file name and returns clang's messages,
// and its error when the source does not compile.
func (c objcChecker) check(t *testing.T, name, source string) (messages string, err error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	compile := exec.Command(c.toolchain.clang, "-isysroot", c.toolchain.sdk,
		"-fsyntax-only", "-fobjc-arc", "-fmodules", "-fmodules-cache-path="+c.moduleCache,
		"-I", c.includeDirectory, path)
	output, err := compile.CombinedOutput()
	return string(output), err
}
