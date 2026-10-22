# satlink on Android

This guide is for Kotlin and Java developers who want an Iridium handset in
their Android app: its status, SMS and calls, and an IP link that only the app
can use. The library is the Go code of package `mobile`, bound with gomobile.
Its [package documentation](https://pkg.go.dev/github.com/anhydrous99/satlink/mobile)
is the API reference.

## Build the library

```sh
ANDROID_HOME=… ANDROID_NDK_HOME=… ./scripts/build-android.sh
```

The script needs:

- the Android SDK, for `android.jar`;
- the NDK;
- a JDK (21 and 27 both accept gomobile's Java 8 target).

It builds the `go.mod`-pinned `gobind` itself. It writes
`android/build/generated/satlink.aar`, which holds:

- the Go code, for armeabi-v7a, arm64-v8a and x86_64, from API level 21;
- the Java bindings, in package `io.github.anhydrous99.satlink.mobile`. The
  functions and constants are on class `Mobile`.

## Add it to your app

1. Add the `.aar` to your app module, and add `android/src/main/` to its
   sources. That directory holds the USB transport (`AndroidUsbTransport.kt`),
   usage sketches (`Example.kt`) and the device filter
   (`res/xml/iridium_device_filter.xml`).
2. Declare USB host support in the manifest:

   ```xml
   <uses-feature android:name="android.hardware.usb.host" />
   ```

3. To launch on plug-in, reference the device filter from an `<intent-filter>`
   for `android.hardware.usb.action.USB_DEVICE_ATTACHED`.
4. Get USB permission for the device with `UsbManager.requestPermission` before
   opening it.

## Use it

Apps cannot open `/dev/ttyACM*`. Instead, `AndroidUsbTransport` talks to the
handset through `UsbManager`, and the library runs over it.

```kotlin
// One for the whole app: later connections resume their TLS session,
// ~1 KB of airtime instead of 3–5 KB.
val tls = TLSConnector(TLSSettings())

AndroidUsbTransport.open(usbManager, device).use { transport ->
    val handset = IridiumHandset(transport)
    try {
        val settings = LinkSettings().apply { allow("relay.example.com:443") }
        try {
            val link = Mobile.establishLinkWithHandset(handset, settings, null, null)
            try {
                val connection = link.openTLS("relay.example.com", 443, tls, 120_000)
                try {
                    connection.send("hello".toByteArray(), 60_000)
                    val reply = connection.receive(4096, 120_000)
                } finally {
                    connection.close()
                }
            } finally {
                link.hangUp(120_000) // while the transport is open: the hang-up goes through it
            }
        } catch (e: Exception) {
            when (Mobile.asSatlinkError(e)?.kind) {
                // The call may still be up: end it, through the transport still open.
                Mobile.SatlinkErrorHangUpFailed, Mobile.SatlinkErrorInternal -> handset.reinitialize()
                else -> throw e
            }
        }
        handset.sendSMS("+881631234567", "back on SMS") // the link gave the handset back
    } finally {
        handset.close()
    }
}
```

`Example.kt` has fuller sketches.

**Threads.** Every call blocks: call setup takes about 30 s, and SMS submission
can take over a minute. Call them off the main thread.

**Closing.** gomobile objects are not `AutoCloseable`, so `use {}` does not
apply to them. Close each one in a `finally` block, and hang up a link in one.
`hangUp` closes the link and returns its state: `LinkStateClosed`, with the
reason it ended, or the state so far if it is still closing.

**One user at a time.** `establishLinkWithHandset` lends the handset to the
link until its call ends. Meanwhile the handset's methods throw
`SatlinkErrorHandsetBusy`. Closing a lent handset leaves the port to its link
until the call has ended. An app that only uses data can call
`Mobile.establishLink(transport, …)` instead.

**State changes.** A `LinkListener`, passed to the establish functions, hears
every change of the link's state.

## Ending a call that may still be up

> [!CAUTION]
> A call left up keeps billing. **Android sends nothing when an app is killed**,
> so the call stays up until the app next opens the handset.

Next time, `AndroidUsbTransport.open()` pulses DTR, and making an
`IridiumHandset` ends any data call still up. If that fails, opening the handset
or the link throws `SatlinkErrorHangUpFailed`.

After these errors a call may still be up. This is how to end it:

| The app has | Thrown | End the call with |
|---|---|---|
| an `IridiumHandset`, lent with `establishLinkWithHandset` or not | `SatlinkErrorHangUpFailed` from `hangUp`, `establishLinkWithHandset` or `answer` (a data call answered by mistake). `SatlinkErrorInternal` from `establishLinkWithHandset`: a fault inside the library, after which no one knows whether a call is up | `handset.reinitialize()`. Until it succeeds, the handset's other methods throw `SatlinkErrorHangUpFailed`, and so does `reinitialize` while it cannot end the call |
| the transport, its link made with `Mobile.establishLink` | `SatlinkErrorHangUpFailed` from `hangUp` or `establishLink`. `SatlinkErrorInternal` from `establishLink` | a new `IridiumHandset` on the transport, still open, then its `hangUp()`. Making it ends a data call still up, and `hangUp` ends one its firmware cannot report |
| only the transport, as `IridiumHandset(transport)` failed | `SatlinkErrorHangUpFailed`: the handset does not answer, and a data call it may be relaying could not be ended | a new `IridiumHandset` on the transport, later |

The rules that go with the table:

- Catch these errors while the transport is open, before the handset is closed.
- Hang up before the transport closes, even after an exception. A link merely
  dropped as its transport closes may leave the call up.
- A closed `IridiumHandset` refuses further use. Make a new one on the
  still-open transport to talk to the handset again.
- Power-cycling the handset is the last resort. Then open the transport and the
  handset anew.

`Mobile.asSatlinkError(e)` gives an exception's details, such as its `kind`.
With the handset's default `+CVHU=2`, a DTR drop ends a voice call too. The
link's limits are enforced by the app, so they stop when it does.

## Cancelling and partial sends

- **Cancelling setup.** Pass a `Cancellation` as the establish function's last
  argument. `cancellation.cancel()`, from another thread such as a cancel
  button's, stops the setup. A dial still in progress is aborted, and a call
  that came up is hung up. The establish function then throws
  `SatlinkErrorCancelled`. Use a new `Cancellation` for each link.
- **Partial sends.** A `send` that times out cannot say how much it queued. Use
  `sendSome`, which returns that count, so exactly the rest can be sent again.
  `abort()` resets the connection, so the peer cannot mistake a part for the
  whole.
- **TLS.** What `sendSome` took may still wait in the TLS session. Call
  `waitAcknowledged` before `hangUp` to send it, as closing the link drops it.
  `TLSConnection.shutdownWrite` sends the TLS goodbye. Sending fails from then
  on, even after a timeout, and calling it again finishes the goodbye.
  `close()` only releases the object.
- **Threads per connection.** Receive from one thread and send from one thread
  at a time. The calls in one direction share one deadline.
- **Settings.** Each establish call reads its `LinkSettings` once, as it starts.

## Equality

gobind's `equals` and `hashCode` compare exported fields only:

- **Handles** (`IridiumHandset`, `SatelliteLink`, the connections,
  `TLSConnector`, `Cancellation` and the lists) have none, so each equals every
  other of its class. Compare them with `===`. Keep them in identity-based
  collections such as `IdentityHashMap`, not in a `HashSet` or as map keys.
  Wrap one before holding it in a `StateFlow`, which ignores a value equal to
  the one it holds.
- **Settings** compare without what `allow`, `addHost` and the other `add`
  methods added, so do not use them as keys.
- **Records** compare the lists they hold as lists do, which is always equal.
  The `Data` of `Received` and `Datagram` compares by reference: compare
  contents instead.
