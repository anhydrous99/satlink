# satlink in macOS apps

This guide is for Swift and Objective-C developers who want an Iridium handset
in a macOS app. The framework is the Go code of package `mobile`, bound with
gomobile. Its [package documentation](https://pkg.go.dev/github.com/anhydrous99/satlink/mobile)
is the API reference.

## Build the framework

```sh
./scripts/build-bindings.sh
```

The script needs a full Xcode, as gomobile refuses the Command Line Tools
alone. It writes `build/bindings/Satlink.xcframework`, with serial-port access
included. Every name in it starts with `SatlinkMobile`, such as
`SatlinkMobileSatelliteLink`.

## Open a handset or a link

1. Find the handsets with `SatlinkMobileDiscoverSerialHandsets(&error)`.
2. Open one with `SatlinkMobileOpenSerialHandset(path, baud, &error)`, which
   also initialises it.
3. Or bring a link up directly with `SatlinkMobileEstablishSerialLink(…)`.

Every call blocks: call setup takes about 30 s, and SMS submission can take
over a minute. Call them off the main thread. When done, call `Close` on a
handset or connection, with `defer` in Swift, and `HangUp` on a link.

## Make objects only with the package functions

Make objects with the package functions, such as `SatlinkMobileNewIridiumHandset`
and `SatlinkMobileNewTLSConnector`. These report errors through their `NSError`
argument, whereas the generated `init` initializers return nil and drop the
error.

`SatlinkMobileIridiumHandset`, `SatlinkMobileTLSConnector` and the interface
classes (`SatlinkMobileByteTransport`, `SatlinkMobileLinkListener`,
`SatlinkMobileLogSink`) inherit `init` and `new` from NSObject. An object made
that way has no Go side, and its first use raises an Objective-C exception that
ends the app. So the build script marks those initializers unavailable, and the
compiler refuses them. Other classes get a no-argument initializer whose object
is not usable: its methods fail with `SatlinkErrorInvalidArgument`.

## Implementing protocols

To implement an interface, such as `ByteTransport`, `LinkListener` or
`LogSink`, adopt its protocol in a class of your own. Swift names the protocols
`SatlinkMobileByteTransportProtocol` and so on. Don't instantiate the matching
class: it stands for a Go implementation.

## When a hang-up fails

> [!CAUTION]
> After `SatlinkErrorHangUpFailed`, the call may still be up and billing.

A serial link keeps its port until its call has ended, and only one program may
open the port. After `SatlinkErrorHangUpFailed` from a serial link's `HangUp`,
the link closes the port a moment later. Closing it drops DTR, which should end
the call. `SatlinkMobileOpenSerialHandset` on the same path then also ends a
data call still up. Power-cycling the handset is the last resort.

## Equality

In Objective-C and Swift, every object compares by identity, records included.

## Not supported

- **iOS:** iPhones don't expose USB CDC devices to apps.
- **Kotlin or Java on the desktop:** gomobile targets Android and Apple
  platforms only.
