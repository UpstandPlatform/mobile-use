package org.mobileuse.companion

import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import com.google.zxing.qrcode.QRCodeWriter
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class PairingTest {
    @Test
    fun screenFrameWebSocketKeepsTheSecureHttpUrlForUpgrade() {
        assertEquals(
            "https://mobile-use.example.com/companion/sessions/session-123/frames",
            screenFrameStreamUrl("https://mobile-use.example.com/", "session-123"),
        )
    }

    @Test
    fun parseLanPairingUriAndQrRoundTrip() {
        val pin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
        val uri = "mobile-use://pair?o=https%3A%2F%2F192.168.1.42%3A8444&c=K7M9-P4X2&p=$pin&n=Workstation&m=lan"
        val matrix = QRCodeWriter().encode(uri, BarcodeFormat.QR_CODE, 200, 200)
        val pixels = IntArray(matrix.width * matrix.height) { idx ->
            val x = idx % matrix.width
            val y = idx / matrix.width
            if (matrix.get(x, y)) 0xFF000000.toInt() else 0xFFFFFFFF.toInt()
        }
        val decoded = QRCodeReader().decode(
            BinaryBitmap(HybridBinarizer(RGBLuminanceSource(matrix.width, matrix.height, pixels)))
        )
        val parsed = parsePairingUri(decoded.text)
        assertEquals("https://192.168.1.42:8444", parsed.origin)
        assertEquals("K7M9P4X2", parsed.code)
        assertEquals(pin, parsed.certPin)
        assertEquals("Workstation", parsed.hostName)
        assertEquals("lan", parsed.mode)
        assertEquals("01:23:45:67:89:AB:CD:EF", formatShortFingerprint(parsed.certPin))
    }

    @Test
    fun parseRemotePairingUri() {
        val uri = "mobile-use://pair?o=https%3A%2F%2Fmobile-use.example.com&c=k7m9p4x2&m=remote"
        val parsed = parsePairingUri(uri)
        assertEquals("https://mobile-use.example.com", parsed.origin)
        assertEquals("K7M9P4X2", parsed.code)
        assertEquals("", parsed.certPin)
        assertEquals("remote", parsed.mode)
    }

    @Test
    fun rejectInvalidUrisAndCodes() {
        assertThrows(IllegalArgumentException::class.java) {
            parsePairingUri("https://example.com/pair?o=https://x&c=K7M9P4X2")
        }
        assertThrows(IllegalArgumentException::class.java) {
            parsePairingUri("mobile-use://pair?o=http://192.168.1.1:8444&c=K7M9P4X2")
        }
        assertThrows(IllegalArgumentException::class.java) {
            normalizePairingCode("INVALID")
        }
        assertThrows(IllegalArgumentException::class.java) {
            normalizeCertPin("not-hex")
        }
    }
}
