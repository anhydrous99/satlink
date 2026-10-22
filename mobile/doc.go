// Package mobile is the satlink API for Android (Kotlin, Java) and Apple
// platforms (Swift, Objective-C), bound with gomobile. The guides in docs/
// show how to build and use it: docs/android.md and docs/macos.md. The
// build commands are in scripts/:
//
//	gomobile bind -target=android/arm,android/arm64,android/amd64 -javapkg io.github.anhydrous99.satlink ./mobile
//	gomobile bind -target=macos -prefix Satlink ./mobile
//
// Java classes are in package io.github.anhydrous99.satlink.mobile, with
// the functions and constants on class Mobile. Objective-C and Swift names
// start with SatlinkMobile, such as SatlinkMobileSatelliteLink.
//
// On Android, apps cannot open /dev/ttyACM*. Instead the app opens the
// device with UsbManager and passes a [ByteTransport] implemented in Kotlin
// (see AndroidUsbTransport.kt). On desktop builds, macOS included,
// [OpenSerialHandset] and [EstablishSerialLink] open the serial port
// directly.
//
// [IridiumHandset] covers the handset itself: status, SMS and calls.
// [SatelliteLink] is the app-private IP link, with [TCPConnection],
// [TLSConnection] and [UDPConnection] over it. A [Cancellation] stops a link
// being set up.
//
// Every call blocks, so call them off the main thread. Call setup takes
// about half a minute, and SMS submission can take over a minute.
//
// # Binding rules
//
// The API keeps to what gomobile can bind: signed integers, bool, string,
// []byte, pointers to structs, interfaces, and error as the last result.
// Hence:
//   - Enumerations are int constants named after the enumeration, such as
//     SMSFilterUnread or LinkStateUp.
//   - Lists are small types with Len and Get, such as [StringList]. Settings
//     collect list entries through methods, such as [LinkSettings.Allow].
//   - Optional values are "" or 0, or a nil pointer, as each one's
//     documentation says. Nothing else is ever nil: no function returns a
//     nil pointer or an empty byte array with a nil error, and lists
//     returned are empty rather than nil.
//   - The end of a stream is a flag of [Received]. An empty datagram is a
//     [Datagram] whose Data is null, because gomobile hands an empty byte
//     array to Java as null and to Swift as nil.
//   - Timeouts are int64 milliseconds, and negative ones count as zero.
//
// # Integers
//
// gobind passes a Go int through a 32-bit C int on armeabi-v7a and on every
// Apple platform, where a larger value wraps before this package sees it.
// So timeouts, the link settings' seconds and byte counts are int64. The
// other int values (lengths, ports, indices, levels and enumerations) never
// need more than 31 bits.
//
// # Objects
//
// Types with a constructor (NewIridiumHandset, NewLinkSettings, …) are
// created with it. In Java and Kotlin, it is the class's constructor, which
// throws the error. In Objective-C and Swift, call the package functions,
// such as SatlinkMobileNewIridiumHandset, which report the error through
// their NSError argument. The generated init initializers return nil and
// drop the error.
//
// gomobile gives every other type a no-argument constructor of its own,
// such as Java's new SatelliteLink(). Its object is not usable: its methods
// fail with SatlinkErrorInvalidArgument. IridiumHandset and TLSConnector,
// whose constructors take arguments, get none.
//
// In Objective-C and Swift, though, IridiumHandset and TLSConnector inherit
// NSObject's init and new. So do ByteTransport, LinkListener and LogSink,
// the classes that stand for Go implementations of those interfaces. An
// object made so has no Go side: using it, or passing it to a function,
// raises an Objective-C exception, which ends the app. So
// scripts/build-bindings.sh marks those initializers unavailable, and the
// compiler refuses them. To implement an interface, adopt its protocol in a
// class of your own. Swift names the protocols
// SatlinkMobileByteTransportProtocol and so on.
//
// A nil object or interface argument (settings, transport, handset,
// connector, sink) fails with SatlinkErrorInvalidArgument, "<name> must not
// be nil". Only a link's listener and cancellation may be nil.
//
// gomobile objects are not AutoCloseable, so Kotlin's use {} does not apply.
// When done, call Close on an IridiumHandset or a connection, and HangUp,
// which closes it, on a SatelliteLink. Do it in a finally block (Kotlin) or
// with defer (Swift). Close releases the object without waiting for a call
// or transfer to finish, and it is idempotent. A closed IridiumHandset or
// connection refuses further use with SatlinkErrorInvalidArgument.
//
// # Equality
//
// In Java and Kotlin, gobind gives every class equals and hashCode methods
// that compare its exported fields and nothing else:
//   - IridiumHandset, SatelliteLink, TCPConnection, TLSConnection,
//     UDPConnection, TLSConnector, Cancellation and the lists have no
//     exported fields. So each equals every other of its class, with the
//     same hash code: compare them by identity.
//   - LinkSettings and TLSSettings compare without what Allow, AddHost and
//     the Add methods added.
//   - Records compare the lists they hold as the lists do, and the Data of
//     Received and Datagram by reference: compare their contents.
//
// In Objective-C and Swift, every object compares by identity, records
// included.
//
// # Errors
//
// A failing call throws an exception (Java, Kotlin) or returns an NSError
// (Objective-C, Swift) whose message is the error's text. [AsSatlinkError]
// recovers the details: pass it the caught error (in Java, the exception is
// a go.error) to get a [SatlinkError], whose Kind is one of the
// SatlinkError constants. Faults inside the library are reported as errors
// of kind SatlinkErrorInternal rather than ending the app. One from an
// EstablishLink function may leave a call up (see SatlinkErrorInternal).
package mobile
