#!/usr/bin/env bash
# Mark NSObject's init and new unavailable, in the gobind Objective-C
# headers given, for every class that declares no argument-less init: those
# whose Go constructor takes arguments (IridiumHandset, TLSConnector), for
# which gobind writes only an init: that takes them, and those that stand
# for a Go implementation of an interface (ByteTransport, LinkListener,
# LogSink). An object made with the init a class inherits has
# no Go side, and its first use raises an Objective-C exception that ends
# the app; marked unavailable, the compiler refuses it, as Rust's Swift
# bindings had no such initializer. build-bindings.sh runs this on the
# framework's headers after gomobile has compiled the bindings: the mark
# changes what apps may write, not the compiled code.
#
# Usage: scripts/mark-unusable-inits.sh HEADER...
set -euo pipefail

if [ $# -eq 0 ]; then
    echo "usage: $0 HEADER..." >&2
    exit 2
fi

# Each class of a gobind header is an @interface block. The mark goes right
# after its initWithRef:, which every one declares; a class whose init is a
# constructor of its own declares that init in the block too, and so does a
# class marked already, which is left as it is. A header in any other shape
# fails, rather than leaving a class unmarked. The mark names new as well,
# which clang would refuse anyway once init is unavailable, so that readers
# of the header see it.
read -r -d '' mark_classes <<'AWK' || true
/^@interface [A-Za-z0-9_]+ : NSObject <goSeqRefInterface/ {
    in_class = 1; class = $2; lines = 0; anchor = 0; has_init = 0; classes++
}
!in_class { print; next }
{
    block[++lines] = $0
    if ($0 ~ /^- \(nonnull instancetype\)initWithRef:/) anchor = lines
    if ($0 ~ /instancetype\)init( NS_UNAVAILABLE)?;$/) has_init = 1
    if ($0 != "@end") next
    if (!anchor) {
        printf "%s: the class %s declares no initWithRef:\n", FILENAME, class > "/dev/stderr"
        failed = 1
        exit
    }
    for (i = 1; i <= lines; i++) {
        print block[i]
        if (i == anchor && !has_init) {
            print "- (nonnull instancetype)init NS_UNAVAILABLE;"
            print "+ (nonnull instancetype)new NS_UNAVAILABLE;"
        }
    }
    in_class = 0
}
END {
    if (failed) exit 1
    if (!classes) {
        printf "%s: no gobind class found\n", FILENAME > "/dev/stderr"
        exit 1
    }
}
AWK

for header in "$@"; do
    # Written over the header only once all of it is marked, which keeps the
    # file's permissions.
    marked=$(awk "$mark_classes" "$header")
    printf '%s\n' "$marked" > "$header"
done
