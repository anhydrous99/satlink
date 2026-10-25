package io.github.anhydrous99.satlink

import android.hardware.usb.UsbConstants
import android.hardware.usb.UsbDevice
import android.hardware.usb.UsbDeviceConnection
import android.hardware.usb.UsbEndpoint
import android.hardware.usb.UsbInterface
import android.hardware.usb.UsbManager
import android.os.SystemClock
import io.github.anhydrous99.satlink.mobile.ByteTransport
import io.github.anhydrous99.satlink.mobile.Mobile

/**
 * [ByteTransport] over Android USB host mode for Iridium handsets.
 *
 * The handset is a USB CDC-ACM device (the official Windows driver falls back to
 * the stock `usbser.sys`), so this talks to its bulk endpoints directly and sends
 * the standard CDC line-coding / control-line requests. No root or kernel driver
 * is needed.
 *
 * The caller must already hold USB permission for the device
 * (see `UsbManager.requestPermission`).
 *
 * gomobile binds no exception subclasses: failures are thrown as plain
 * [Exception]s, whose message reaches the library as the transport's error.
 */
class AndroidUsbTransport private constructor(
    private val connection: UsbDeviceConnection,
    private val interfaces: List<UsbInterface>,
    private val controlIndex: Int,
    private val inEndpoint: UsbEndpoint,
    private val outEndpoint: UsbEndpoint,
) : ByteTransport, AutoCloseable {

    private val buffer = ByteArray(maxOf(inEndpoint.maxPacketSize, 64) * 8)
    private var pending = ByteArray(0)

    override fun write(data: ByteArray) {
        var offset = 0
        while (offset < data.size) {
            val n = connection.bulkTransfer(outEndpoint, data, offset, data.size - offset, WRITE_TIMEOUT_MS)
            if (n <= 0) throw Exception("USB write failed ($n)")
            offset += n
        }
    }

    /**
     * [timeoutMillis] is at most Int.MAX_VALUE, the library makes sure, so it fits `bulkTransfer`.
     * Throws when the transfer fails, as every one does once the handset is unplugged:
     * an empty array would have the library read again at once, keeping a CPU busy.
     */
    override fun read(maxLen: Long, timeoutMillis: Long): ByteArray {
        if (pending.isEmpty()) {
            val started = SystemClock.elapsedRealtime()
            val n = connection.bulkTransfer(inEndpoint, buffer, buffer.size, timeoutMillis.toInt())
            // A negative result is how bulkTransfer reports a timeout, and a failure too.
            if (n < 0 && failedEarly(timeoutMillis, SystemClock.elapsedRealtime() - started)) {
                throw Exception("USB read failed ($n)")
            }
            if (n <= 0) return ByteArray(0)
            pending = buffer.copyOf(n)
        }
        val out = pending.copyOf(minOf(maxLen, pending.size.toLong()).toInt())
        pending = pending.copyOfRange(out.size, pending.size)
        return out
    }

    override fun raiseDTR() = changeControlLines(DTR_RTS)

    override fun dropDTR() = changeControlLines(RTS_ONLY)

    /** Sets the control lines, failing if the handset does not take the change. */
    private fun changeControlLines(lines: Int) {
        if (setControlLines(connection, controlIndex, lines) < 0) {
            throw Exception("the handset did not take the DTR change")
        }
    }

    /**
     * Drops DTR before letting go of the device. With `AT&D2`, which data
     * calls set, the handset should then end any data call still up — and,
     * with its default `+CVHU=2`, a voice call too. Neither is verified on
     * hardware over USB yet. Android sends nothing when an app is killed, so
     * a call can outlive a killed app until the handset is next opened and
     * initialised, which ends a data call still up — online, or held in
     * online-command mode where the handset reports it with `AT+CPAS`.
     */
    override fun close() {
        setControlLines(connection, controlIndex, LINES_DOWN)
        interfaces.forEach { connection.releaseInterface(it) }
        connection.close()
    }

    companion object {
        private const val WRITE_TIMEOUT_MS = 2000
        private const val CDC_REQUEST_TYPE = 0x21 // host-to-device | class | interface
        private const val SET_LINE_CODING = 0x20
        private const val SET_CONTROL_LINE_STATE = 0x22
        private const val DTR_RTS = 0x03
        private const val RTS_ONLY = 0x02
        private const val LINES_DOWN = 0x00
        private const val CONTROL_TIMEOUT_MS = 1000
        /** Long enough for the handset to notice: its DTR debounce (S25) is 50 ms. */
        private const val DTR_DROP_MS = 150L
        /**
         * A negative read result that comes back in under 1/[FAILURE_DIVISOR] of
         * its timeout is a failure: a failed read comes back at once, a timed-out
         * one at most a kernel clock tick (10 ms or less) early.
         */
        private const val FAILURE_DIVISOR = 4
        /**
         * The shortest read timeout that tells a failure from a timeout: a
         * quarter of it is more than a clock tick short of it. A link reads
         * with 20 ms timeouts.
         */
        private const val SHORTEST_JUDGED_TIMEOUT_MS = 20L

        /** True when [device] carries Iridium's USB vendor ID. */
        fun isIridiumHandset(device: UsbDevice): Boolean =
            device.vendorId == Mobile.iridiumUSBVendorID().toInt()

        /**
         * Open the handset's CDC-ACM data channel. DTR is held low briefly
         * first: if a killed app left it high, the handset (with `AT&D2`)
         * ends the data call that app left up — and, with its default
         * `+CVHU=2`, a voice call in progress too. Over USB this is not yet
         * verified on hardware; initialising the handset, as `IridiumHandset`
         * and `Mobile.establishLink` do, also ends a data call left up — or,
         * if it cannot, throws an exception whose `Mobile.asSatlinkError(e)?.kind`
         * is `Mobile.SatlinkErrorHangUpFailed` — and `hangUp` on the handset
         * ends one its firmware cannot report.
         */
        fun open(manager: UsbManager, device: UsbDevice, baud: Int = 19_200): AndroidUsbTransport {
            val all = (0 until device.interfaceCount).map { device.getInterface(it) }
            val data = all.firstOrNull { it.interfaceClass == UsbConstants.USB_CLASS_CDC_DATA && bulkPair(it) != null }
                ?: all.firstOrNull { bulkPair(it) != null }
                ?: throw Exception("no bulk IN/OUT interface on ${device.deviceName}")
            val control = all.firstOrNull { it.interfaceClass == UsbConstants.USB_CLASS_COMM }
            val (inEp, outEp) = bulkPair(data)!!

            val connection = manager.openDevice(device)
                ?: throw Exception("cannot open ${device.deviceName}; is USB permission granted?")
            val claimed = listOfNotNull(control, data)
            // Whatever goes wrong from here, the device must not stay claimed.
            try {
                for (iface in claimed) {
                    if (!connection.claimInterface(iface, true)) {
                        throw Exception("cannot claim interface ${iface.id}")
                    }
                }
                val controlIndex = (control ?: data).id
                setLineCoding(connection, controlIndex, baud)
                setControlLines(connection, controlIndex, LINES_DOWN)
                Thread.sleep(DTR_DROP_MS)
                setControlLines(connection, controlIndex, DTR_RTS)
                return AndroidUsbTransport(connection, claimed, controlIndex, inEp, outEp)
            } catch (e: Throwable) {
                claimed.forEach { connection.releaseInterface(it) }
                connection.close()
                throw e
            }
        }

        /** 8 data bits, no parity, 1 stop bit; some firmwares ignore it, which is fine. */
        private fun setLineCoding(connection: UsbDeviceConnection, controlIndex: Int, baud: Int) {
            val lineCoding = byteArrayOf(
                (baud and 0xFF).toByte(), (baud shr 8 and 0xFF).toByte(),
                (baud shr 16 and 0xFF).toByte(), (baud shr 24 and 0xFF).toByte(),
                0, // 1 stop bit
                0, // no parity
                8, // data bits
            )
            connection.controlTransfer(
                CDC_REQUEST_TYPE, SET_LINE_CODING, 0, controlIndex, lineCoding, lineCoding.size, CONTROL_TIMEOUT_MS,
            )
        }

        /** Returns what `controlTransfer` does: negative on failure. */
        private fun setControlLines(connection: UsbDeviceConnection, controlIndex: Int, lines: Int): Int =
            connection.controlTransfer(
                CDC_REQUEST_TYPE, SET_CONTROL_LINE_STATE, lines, controlIndex, null, 0, CONTROL_TIMEOUT_MS,
            )

        private fun bulkPair(iface: UsbInterface): Pair<UsbEndpoint, UsbEndpoint>? {
            val eps = (0 until iface.endpointCount).map { iface.getEndpoint(it) }
                .filter { it.type == UsbConstants.USB_ENDPOINT_XFER_BULK }
            val input = eps.firstOrNull { it.direction == UsbConstants.USB_DIR_IN } ?: return null
            val output = eps.firstOrNull { it.direction == UsbConstants.USB_DIR_OUT } ?: return null
            return input to output
        }

        /**
         * True when a negative `bulkTransfer` result, [elapsedMillis] into a
         * read of [timeoutMillis], is a failure. After a timeout shorter than
         * [SHORTEST_JUDGED_TIMEOUT_MS], it is taken for a timeout.
         */
        private fun failedEarly(timeoutMillis: Long, elapsedMillis: Long): Boolean =
            timeoutMillis >= SHORTEST_JUDGED_TIMEOUT_MS && elapsedMillis < timeoutMillis / FAILURE_DIVISOR
    }
}
