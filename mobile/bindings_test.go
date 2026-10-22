package mobile_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The bindings as gomobile makes them: the go.mod-pinned gobind, run per
// target as scripts/build-android.sh and scripts/build-bindings.sh run it
// through gomobile, and the package compiled per target. gobind leaves out
// what it cannot bind, saying so only in a comment of the generated code,
// so these tests fail on any such comment. gobind type-checks without cgo,
// so only compiling shows that the serial library builds for macOS.

// boundPackage is the package the bindings are for.
const boundPackage = "github.com/anhydrous99/satlink/mobile"

// The gomobile flags of the scripts.
const (
	// javaPackage is the Java package that gomobile appends "mobile" to.
	javaPackage = "io.github.anhydrous99.satlink"
	// objcPrefix starts every Objective-C and Swift name.
	objcPrefix = "Satlink"
)

// skippedMarker starts gobind's comment on everything it left out.
const skippedMarker = "// skipped "

// javaSourceDirectory is where gobind writes the Java classes.
const javaSourceDirectory = "java/io/github/anhydrous99/satlink/mobile"

// objcHeader is the Objective-C header gobind writes.
const objcHeader = "src/gobind/" + objcPrefix + "Mobile.objc.h"

// serialObjCSymbol is a symbol of the serial API in objcHeader.
const serialObjCSymbol = objcPrefix + "MobileSerialHandsetPort"

// bindingTarget is a platform gomobile binds the package for.
type bindingTarget struct {
	name string
	// environment is what gomobile sets for the target, added to the
	// test's own: the operating system, the architecture and cgo.
	environment []string
	// tags are the build tags gomobile adds for the target.
	tags string
	// gobindFlags choose the bindings' language and names.
	gobindFlags []string
	// serialFile is the generated file, relative to the output, that holds
	// serialSymbol, a symbol of the serial API, when that API is bound.
	serialFile, serialSymbol string
	// hasSerialAPI reports whether the desktop serial API is bound.
	hasSerialAPI bool
	// unavailable says why the target cannot be built here; "" when it
	// can.
	unavailable string
}

// androidTarget is Android's, on its narrowest ABI, armeabi-v7a, where a
// Go int has 32 bits.
var androidTarget = bindingTarget{
	name:         "android",
	environment:  []string{"GOOS=android", "GOARCH=arm", "CGO_ENABLED=0"},
	gobindFlags:  []string{"-lang=go,java", "-javapkg=" + javaPackage},
	serialFile:   javaSourceDirectory + "/Mobile.java",
	serialSymbol: "discoverSerialHandsets",
}

// iosTarget is iOS's: GOOS=ios with the ios tag, without the serial API.
var iosTarget = bindingTarget{
	name:         "ios",
	environment:  []string{"GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=0"},
	tags:         "ios",
	gobindFlags:  []string{"-lang=go,objc", "-prefix=" + objcPrefix},
	serialFile:   objcHeader,
	serialSymbol: serialObjCSymbol,
}

// maccatalystTarget is Mac Catalyst's, an iOS app running on macOS: GOOS=ios
// with the macos and maccatalyst tags, as gomobile's -target=maccatalyst
// builds it, without the serial API.
var maccatalystTarget = bindingTarget{
	name:         "maccatalyst",
	environment:  []string{"GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=0"},
	tags:         "macos,maccatalyst",
	gobindFlags:  []string{"-lang=go,objc", "-prefix=" + objcPrefix},
	serialFile:   objcHeader,
	serialSymbol: serialObjCSymbol,
}

// macOSToolchain is the macOS SDK and its clang.
type macOSToolchain struct {
	// sdk is the SDK's path, for -isysroot.
	sdk string
	// clang is the SDK's C and Objective-C compiler.
	clang string
}

// findMacOSToolchain returns the macOS SDK and its clang, or why they are
// not here: "" when they are.
func findMacOSToolchain() (toolchain macOSToolchain, unavailable string) {
	if runtime.GOOS != "darwin" {
		return macOSToolchain{}, "the macOS SDK is only on a Mac"
	}
	sdk, sdkErr := exec.Command("xcrun", "--sdk", "macosx", "--show-sdk-path").Output()
	clang, clangErr := exec.Command("xcrun", "--sdk", "macosx", "--find", "clang").Output()
	if sdkErr != nil || clangErr != nil {
		return macOSToolchain{}, "xcrun finds no macOS SDK"
	}
	return macOSToolchain{sdk: strings.TrimSpace(string(sdk)), clang: strings.TrimSpace(string(clang))}, ""
}

// macosTarget returns macOS's target: GOOS=ios with the macos tag, with
// the serial API, whose port discovery is cgo built with the macOS SDK's
// clang, as gomobile sets them up. (gomobile then compiles the macOS
// archive itself with GOOS=darwin and the macos tag, which the host build
// of these tests covers.) Away from a Mac, or without the SDK, the target
// is unavailable.
func macosTarget(t *testing.T) bindingTarget {
	t.Helper()
	target := bindingTarget{
		name:         "macos",
		tags:         "macos",
		gobindFlags:  []string{"-lang=go,objc", "-prefix=" + objcPrefix},
		serialFile:   objcHeader,
		serialSymbol: serialObjCSymbol,
		hasSerialAPI: true,
	}
	toolchain, unavailable := findMacOSToolchain()
	if unavailable != "" {
		target.unavailable = unavailable
		return target
	}
	sysroot := "-isysroot " + toolchain.sdk
	target.environment = []string{
		"GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=1", "CC=" + toolchain.clang,
		"CGO_CFLAGS=" + sysroot, "CGO_LDFLAGS=" + sysroot,
	}
	return target
}

// bindingTargets returns every platform the package is bound for.
func bindingTargets(t *testing.T) []bindingTarget {
	t.Helper()
	return []bindingTarget{androidTarget, iosTarget, maccatalystTarget, macosTarget(t)}
}

// kotlinUses are the declarations of the Java bindings that the Kotlin
// sources in android/src/main/java/io/github/anhydrous99/satlink use, by
// file, as gobind writes them.
var kotlinUses = map[string][]string{
	"Mobile.java": {
		"public static native SatelliteLink establishLink(ByteTransport transport, LinkSettings settings, LinkListener listener, Cancellation cancellation)",
		"public static native SatelliteLink establishLinkWithHandset(IridiumHandset handset, LinkSettings settings, LinkListener listener, Cancellation cancellation)",
		"public static native SatlinkError asSatlinkError(java.lang.Exception err)",
		"public static native long iridiumUSBVendorID()",
		"public static final long SatlinkErrorTLSTruncated",
		"public static final long SatlinkErrorHangUpFailed",
		"public static final long SatlinkErrorInternal",
	},
	"IridiumHandset.java": {
		"public IridiumHandset(ByteTransport transport)",
		"public native long signalQuality() throws Exception;",
		"public native long sendSMS(String number, String text) throws Exception;",
		"public native void hangUp() throws Exception;",
		"public native void reinitialize() throws Exception;",
		"public native void close() throws Exception;",
	},
	"Cancellation.java": {"public Cancellation()"},
	"LinkSettings.java": {
		"public LinkSettings()",
		"public native void allow(String rule);",
		// Kotlin's idleTimeoutSeconds property needs both.
		"public final native long getIdleTimeoutSeconds();",
		"public final native void setIdleTimeoutSeconds(long v);",
	},
	"SatelliteLink.java": {
		"public native TLSConnection openTLS(String host, long port, TLSConnector connector, long timeoutMillis) throws Exception;",
		"public native LinkState hangUp(long timeoutMillis) throws Exception;",
	},
	"TLSSettings.java":  {"public TLSSettings()"},
	"TLSConnector.java": {"public TLSConnector(TLSSettings settings)"},
	"TLSConnection.java": {
		"public native void send(byte[] data, long timeoutMillis) throws Exception;",
		"public native Received receive(long maxLen, long timeoutMillis) throws Exception;",
		"public native void waitAcknowledged(long timeoutMillis) throws Exception;",
		"public native void close() throws Exception;",
	},
	"Received.java":     {"public final native byte[] getData();", "public final native boolean getFinished();"},
	"SatlinkError.java": {"public final native long getKind();"},
}

// byteTransportMethods are the methods AndroidUsbTransport.kt implements.
// The Java interface must declare exactly these, in gobind's order, as
// Kotlin refuses a class that leaves one out.
var byteTransportMethods = []string{
	"public void dropDTR() throws Exception;",
	"public void raiseDTR() throws Exception;",
	"public byte[] read(long maxLen, long timeoutMillis) throws Exception;",
	"public void write(byte[] data) throws Exception;",
}

// comparedFields are the fields, in their order, that the equals method
// gobind writes for a Java class compares, by class, as the package
// documentation says (Equality): none for the objects and the lists, which
// so equal every other of their class, and the exported fields alone for
// the settings.
var comparedFields = map[string][]string{
	"IridiumHandset":   nil,
	"SatelliteLink":    nil,
	"TCPConnection":    nil,
	"TLSConnection":    nil,
	"UDPConnection":    nil,
	"TLSConnector":     nil,
	"Cancellation":     nil,
	"StringList":       nil,
	"SMSMessageList":   nil,
	"KnownProductList": nil,
	"LinkSettings":     {"Number", "Username", "Password", "MaxCallSeconds", "IdleTimeoutSeconds", "MaxBytes", "CloseGraceSeconds"},
	"TLSSettings":      {"TrustPublicRoots"},
}

// comparedField matches a field that a generated equals reads of the
// object it compares with, such as that.getNumber().
var comparedField = regexp.MustCompile(`that\.get(\w+)\(\)`)

// contextStub stands in for android.content.Context, the one Android class
// the Java bindings refer to (go.Seq.setContext), which gomobile compiles
// against the Android SDK's android.jar.
const contextStub = "package android.content;\n\n/** Stands in for the Android SDK's Context. */\npublic class Context {}\n"

// skipUnlessThorough skips a test that builds tools and code for other
// platforms under -short.
func skipUnlessThorough(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds gobind and the package for other platforms")
	}
}

func TestTheBindingsSkipNothing(t *testing.T) {
	skipUnlessThorough(t)
	gobind := buildGobind(t)
	for _, target := range bindingTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			output := runGobind(t, gobind, target)

			assertNothingSkipped(t, output)
			assertSerialAPI(t, output, target)
		})
	}
}

func TestThePackageBuildsForEveryTarget(t *testing.T) {
	skipUnlessThorough(t)
	goTool := findGoTool(t)
	for _, target := range bindingTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			skipUnavailable(t, target)
			build := exec.Command(goTool, "build", "-tags="+target.tags, boundPackage)
			build.Env = append(os.Environ(), target.environment...)

			if output, err := build.CombinedOutput(); err != nil {
				t.Errorf("building for %s: %v\n%s", target.name, err, output)
			}
		})
	}
}

func TestTheJavaBindingsHaveWhatTheKotlinSourcesUse(t *testing.T) {
	skipUnlessThorough(t)
	output := runGobind(t, buildGobind(t), androidTarget)

	for file, declarations := range kotlinUses {
		source := readGenerated(t, output, filepath.Join(javaSourceDirectory, file))
		for _, declaration := range declarations {
			if !strings.Contains(source, declaration) {
				t.Errorf("%s does not declare %q", file, declaration)
			}
		}
	}
	transport := readGenerated(t, output, filepath.Join(javaSourceDirectory, "ByteTransport.java"))
	if methods := interfaceMethods(transport); !slices.Equal(methods, byteTransportMethods) {
		t.Errorf("ByteTransport.java declares %q, want exactly %q, which AndroidUsbTransport implements", methods, byteTransportMethods)
	}
}

// TestTheJavaBindingsCompareExportedFieldsOnly holds gobind to the equality
// the package documentation warns of, so that the warning is revisited
// should gobind change.
func TestTheJavaBindingsCompareExportedFieldsOnly(t *testing.T) {
	skipUnlessThorough(t)
	output := runGobind(t, buildGobind(t), androidTarget)

	for class, want := range comparedFields {
		source := readGenerated(t, output, filepath.Join(javaSourceDirectory, class+".java"))
		got, found := equalsComparisons(source)
		if !found {
			t.Errorf("%s.java has no equals method", class)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s.equals compares %q, want %q", class, got, want)
		}
	}
}

func TestTheJavaBindingsCompile(t *testing.T) {
	skipUnlessThorough(t)
	javac := findJavac(t)
	output := runGobind(t, buildGobind(t), androidTarget)
	sources := append(javaSources(t, output), writeContextStub(t))

	// gomobile compiles them for Java 8 too, before packing the .aar.
	arguments := append([]string{"--release", "8", "-Xlint:-options", "-d", t.TempDir()}, sources...)
	if messages, err := exec.Command(javac, arguments...).CombinedOutput(); err != nil {
		t.Errorf("javac: %v\n%s", err, messages)
	}
}

// findGoTool returns the go command, or skips the test without one.
func findGoTool(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not on PATH")
	}
	return goTool
}

// findJavac returns a working Java compiler — JAVA_HOME's, or javac on
// PATH — or skips the test without one. macOS puts a javac on PATH that
// only says that no Java is installed.
func findJavac(t *testing.T) string {
	t.Helper()
	javac := "javac"
	if home := os.Getenv("JAVA_HOME"); home != "" {
		javac = filepath.Join(home, "bin", "javac")
	}
	if err := exec.Command(javac, "-version").Run(); err != nil {
		t.Skipf("no working Java compiler (%s: %v); set JAVA_HOME to a JDK", javac, err)
	}
	return javac
}

// buildGobind builds the go.mod-pinned gobind into the test's own
// directory and returns its path. Windows runs only a file with an
// executable's extension, which go build -o does not add.
func buildGobind(t *testing.T) string {
	t.Helper()
	name := "gobind"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	gobind := filepath.Join(t.TempDir(), name)
	build := exec.Command(findGoTool(t), "build", "-o", gobind, "golang.org/x/mobile/cmd/gobind")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building gobind: %v\n%s", err, output)
	}
	return gobind
}

// skipUnavailable skips the test for a target that cannot be built here.
func skipUnavailable(t *testing.T, target bindingTarget) {
	t.Helper()
	if target.unavailable != "" {
		t.Skip(target.unavailable)
	}
}

// runGobind generates target's bindings into a new directory and returns
// it.
func runGobind(t *testing.T, gobind string, target bindingTarget) string {
	t.Helper()
	skipUnavailable(t, target)
	output := t.TempDir()
	arguments := append([]string{}, target.gobindFlags...)
	if target.tags != "" {
		arguments = append(arguments, "-tags="+target.tags)
	}
	generate := exec.Command(gobind, append(arguments, "-outdir", output, boundPackage)...)
	generate.Env = append(os.Environ(), target.environment...)
	if messages, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("gobind for %s: %v\n%s", target.name, err, messages)
	}
	return output
}

// assertNothingSkipped fails the test for every symbol gobind left out of
// the bindings in output.
func assertNothingSkipped(t *testing.T, output string) {
	t.Helper()
	err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(source, []byte("\n")) {
			if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, []byte(skippedMarker)) {
				t.Errorf("%s: %s", filepath.Base(path), trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the bindings: %v", err)
	}
}

// assertSerialAPI fails the test unless the serial API is bound exactly
// where target says it is.
func assertSerialAPI(t *testing.T, output string, target bindingTarget) {
	t.Helper()
	bound := strings.Contains(readGenerated(t, output, target.serialFile), target.serialSymbol)
	if bound != target.hasSerialAPI {
		t.Errorf("the serial API is bound: %t, want %t", bound, target.hasSerialAPI)
	}
}

// readGenerated returns a file gobind generated into output.
func readGenerated(t *testing.T, output, file string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(output, file))
	if err != nil {
		t.Fatalf("reading the generated %s: %v", file, err)
	}
	return string(source)
}

// interfaceMethods returns the method declarations of a Java interface
// that gobind wrote, in their order.
func interfaceMethods(source string) []string {
	var methods []string
	for line := range strings.Lines(source) {
		declaration := strings.TrimSpace(line)
		if strings.HasPrefix(declaration, "public ") && strings.HasSuffix(declaration, ";") {
			methods = append(methods, declaration)
		}
	}
	return methods
}

// equalsComparisons returns the fields, in their order, that the equals
// method of a Java class gobind wrote compares, and whether it has one.
func equalsComparisons(source string) (fields []string, found bool) {
	start := strings.Index(source, "public boolean equals(Object o)")
	end := strings.Index(source, "public int hashCode()")
	if start < 0 || end < start {
		return nil, false
	}
	for _, match := range comparedField.FindAllStringSubmatch(source[start:end], -1) {
		fields = append(fields, match[1])
	}
	return fields, true
}

// javaSources returns the Java files gobind generated into output.
func javaSources(t *testing.T, output string) []string {
	t.Helper()
	var sources []string
	err := filepath.WalkDir(filepath.Join(output, "java"), func(path string, entry fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".java") {
			sources = append(sources, path)
		}
		return err
	})
	if err != nil || len(sources) == 0 {
		t.Fatalf("finding the generated Java: %d files, %v", len(sources), err)
	}
	return sources
}

// writeContextStub writes contextStub where javac looks for its class and
// returns the file's path.
func writeContextStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "android", "content", "Context.java")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making the stub's directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contextStub), 0o644); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}
