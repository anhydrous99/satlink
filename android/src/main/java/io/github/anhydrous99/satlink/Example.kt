package io.github.anhydrous99.satlink

import android.content.Context
import android.hardware.usb.UsbDevice
import android.hardware.usb.UsbManager
import io.github.anhydrous99.satlink.mobile.Cancellation
import io.github.anhydrous99.satlink.mobile.IridiumHandset
import io.github.anhydrous99.satlink.mobile.LinkSettings
import io.github.anhydrous99.satlink.mobile.Mobile
import io.github.anhydrous99.satlink.mobile.SatelliteLink
import io.github.anhydrous99.satlink.mobile.TLSConnection
import io.github.anhydrous99.satlink.mobile.TLSConnector
import io.github.anhydrous99.satlink.mobile.TLSSettings
import java.io.ByteArrayOutputStream

/*
 * Minimal usage sketches. Run them off the main thread: every call blocks on
 * the satellite link (SMS submission or call setup can take a minute or more).
 *
 * gomobile objects are not AutoCloseable, so `use {}` does not apply to them:
 * each is closed in a `finally` block, and a link is hung up in one.
 *
 * Nor do they compare by identity: gobind's `equals` and `hashCode` look at
 * exported fields only, so every handset, link, connection, connector or
 * cancellation equals every other of its class. Compare them with `===`, and
 * keep them in identity-based collections, not in a `HashSet`, as map keys
 * or, without a wrapper, in a `StateFlow`.
 */

/** One for the whole app: later connections resume their TLS session, ~1 KB of airtime instead of 3–5 KB. */
private val tlsConnector: TLSConnector by lazy { TLSConnector(TLSSettings()) }

fun sendTestMessage(context: Context, number: String, text: String): Long {
    val (manager, device) = findHandset(context)
    AndroidUsbTransport.open(manager, device).use { transport ->
        val handset = IridiumHandset(transport)
        try {
            val bars = handset.signalQuality()
            check(bars > 0) { "No satellite signal ($bars/5)" }
            return handset.sendSMS(number, text)
        } finally {
            handset.close()
        }
    }
}

/**
 * Fetch a page over the satellite link. Only `relay.example.com:443` can be
 * reached, and nothing else on the phone can use the link at all. Until the
 * link is up, `cancellation.cancel()` from another thread — a cancel
 * button's — stops it, with SatlinkErrorCancelled and no call left up. If
 * the call could not be hung up, or a fault inside the library left unknown
 * whether it is up, ending it is tried again before the error is passed on.
 */
fun fetchOverSatellite(context: Context, cancellation: Cancellation = Cancellation()): String {
    val (manager, device) = findHandset(context)
    AndroidUsbTransport.open(manager, device).use { transport ->
        val settings = LinkSettings().apply {
            allow("relay.example.com:443")
            idleTimeoutSeconds = 120
        }
        try {
            val link = Mobile.establishLink(transport, settings, null, cancellation)
            try {
                val connection = link.openTLS("relay.example.com", 443, tlsConnector, 120_000)
                try {
                    connection.send("GET / HTTP/1.0\r\nHost: relay.example.com\r\n\r\n".toByteArray(), 60_000)
                    return readResponse(connection)
                } finally {
                    connection.close()
                }
            } finally {
                hangUp(link)
            }
        } catch (e: Exception) {
            if (!mayHaveLeftTheCallUp(e)) throw e
            // A data-only link leaves no handset to reinitialize: one made on
            // the same transport, still open, ends the call.
            runCatching { hangUpThrough(transport) }.onFailure { e.addSuppressed(it) }
            throw e
        }
    }
}

/**
 * SMS and data on one handset: the link borrows it for the length of the
 * call. Meanwhile the handset's own calls fail with SatlinkErrorHandsetBusy,
 * and once the call has ended they work again.
 */
fun postThenText(context: Context, number: String) {
    val (manager, device) = findHandset(context)
    AndroidUsbTransport.open(manager, device).use { transport ->
        val handset = IridiumHandset(transport)
        try {
            post(handset, "hello from the sky\n")
            handset.sendSMS(number, "Posted over the satellite link")
        } finally {
            handset.close()
        }
    }
}

/**
 * Send [message] to the relay over a link that borrows [handset] for the
 * length of the call. A call that could not be hung up, or that a fault
 * inside the library may have left up, is ended by `reinitialize` if it can
 * be, while the transport is still open; the error is passed on either way,
 * its suppressed list empty when the call was ended.
 */
private fun post(handset: IridiumHandset, message: String) {
    val settings = LinkSettings().apply { allow("relay.example.com:443") }
    try {
        val link = Mobile.establishLinkWithHandset(handset, settings, null, null)
        try {
            val connection = link.openTLS("relay.example.com", 443, tlsConnector, 120_000)
            try {
                connection.send(message.toByteArray(), 60_000)
                // Sent means queued: wait until it has arrived.
                connection.waitAcknowledged(120_000)
            } finally {
                connection.close()
            }
        } finally {
            hangUp(link)
        }
    } catch (e: Exception) {
        if (!mayHaveLeftTheCallUp(e)) throw e
        runCatching { handset.reinitialize() }.onFailure { e.addSuppressed(it) }
        throw e
    }
}

/**
 * Whether [e] says that the call may still be up: the hang-up was not
 * confirmed (SatlinkErrorHangUpFailed), or a fault inside the library
 * (SatlinkErrorInternal) left unknown whether a call is up.
 */
private fun mayHaveLeftTheCallUp(e: Exception): Boolean =
    when (Mobile.asSatlinkError(e)?.kind) {
        Mobile.SatlinkErrorHangUpFailed, Mobile.SatlinkErrorInternal -> true
        else -> false
    }

/** End a call left up with a handset made on [transport], which must still be open. */
private fun hangUpThrough(transport: AndroidUsbTransport) {
    val handset = IridiumHandset(transport)
    try {
        handset.hangUp()
    } finally {
        handset.close()
    }
}

/**
 * End the call — even after an exception, and while the transport is still
 * open, as the hang-up goes through it. Allows for the close grace (30 s) and
 * the hang-up itself; if the call may still be up, throws an exception of
 * kind SatlinkErrorHangUpFailed ([mayHaveLeftTheCallUp]).
 */
private fun hangUp(link: SatelliteLink) {
    link.hangUp(120_000)
}

/**
 * The whole response. Many servers close without the TLS goodbye once they
 * have sent everything; then only a matching Content-Length shows that
 * nothing is missing.
 */
private fun readResponse(connection: TLSConnection): String {
    val response = ByteArrayOutputStream()
    val endedProperly = try {
        while (true) {
            val received = connection.receive(4096, 120_000)
            if (received.finished) break
            response.write(received.data)
        }
        true
    } catch (e: Exception) {
        if (Mobile.asSatlinkError(e)?.kind != Mobile.SatlinkErrorTLSTruncated) throw e
        false
    }
    val bytes = response.toByteArray()
    check(endedProperly || hasFullBody(bytes)) { "The response may be cut short" }
    return bytes.toString(Charsets.UTF_8)
}

/** Whether the body is as long as the response's Content-Length says. */
private fun hasFullBody(response: ByteArray): Boolean {
    val text = String(response, Charsets.ISO_8859_1) // one character per byte
    val headerEnd = text.indexOf("\r\n\r\n").takeIf { it >= 0 } ?: return false
    val length = Regex("(?im)^content-length:\\s*(\\d+)\\s*$")
        .find(text.substring(0, headerEnd))
        ?.groupValues?.get(1)?.toIntOrNull()
        ?: return false
    return response.size - (headerEnd + 4) == length
}

private fun findHandset(context: Context): Pair<UsbManager, UsbDevice> {
    val manager = context.getSystemService(Context.USB_SERVICE) as UsbManager
    val device = manager.deviceList.values.firstOrNull { AndroidUsbTransport.isIridiumHandset(it) }
        ?: error("No Iridium handset attached")
    check(manager.hasPermission(device)) { "Request USB permission first" }
    return manager to device
}
