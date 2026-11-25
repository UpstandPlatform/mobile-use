package org.mobileuse.companion

import java.net.URI
import java.net.URLDecoder

private const val PAIRING_ALPHABET = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

data class PairingRequest(
    val origin: String,
    val code: String,
    val certPin: String,
    val hostName: String,
    val mode: String,
)

fun normalizedOrigin(value: String): String {
    val origin = URI(value.trim().trimEnd('/'))
    require(origin.scheme == "https" && origin.host != null && origin.userInfo == null &&
        origin.rawPath.isNullOrEmpty() && origin.rawQuery == null && origin.rawFragment == null) {
        "Enter the HTTPS server address shown in the console."
    }
    return origin.toASCIIString()
}

fun normalizePairingCode(raw: String): String {
    val clean = raw.trim().replace("-", "").replace(" ", "")
    if (clean.length == 8) {
        val upper = clean.uppercase()
        require(upper.all { it in PAIRING_ALPHABET }) { "Enter a valid 8-character pairing code." }
        return upper
    }
    if (clean.length == 32) {
        val lower = clean.lowercase()
        require(lower.all { it in '0'..'9' || it in 'a'..'f' }) { "Enter a valid pairing code." }
        return lower
    }
    throw IllegalArgumentException("Enter the 8-character pairing code shown in the console.")
}

fun normalizeCertPin(raw: String): String {
    val clean = raw.trim().replace(":", "").replace(" ", "").lowercase()
    if (clean.isEmpty()) return ""
    require(clean.length == 64 && clean.all { it in '0'..'9' || it in 'a'..'f' }) {
        "Certificate fingerprint must be a 64-character SHA-256 hex digest."
    }
    return clean
}

fun formatShortFingerprint(hex: String): String {
    val clean = hex.trim().uppercase()
    if (clean.length < 16) return clean
    return (0 until 16 step 2).joinToString(":") { clean.substring(it, it + 2) }
}

fun parsePairingUri(raw: String): PairingRequest {
    val uri = URI(raw.trim())
    require(uri.scheme == "mobile-use" && uri.host == "pair") {
        "This QR code is not a mobile-use pairing code."
    }
    val params = mutableMapOf<String, String>()
    val query = uri.rawQuery.orEmpty()
    for (part in query.split('&')) {
        if (part.isEmpty()) continue
        val eq = part.indexOf('=')
        if (eq <= 0) continue
        val key = URLDecoder.decode(part.substring(0, eq), "UTF-8")
        val value = URLDecoder.decode(part.substring(eq + 1), "UTF-8")
        params[key] = value
    }
    val origin = normalizedOrigin(params["o"].orEmpty())
    val code = normalizePairingCode(params["c"].orEmpty())
    val pin = normalizeCertPin(params["p"].orEmpty())
    val hostName = params["n"].orEmpty().trim().take(64)
    val mode = if (params["m"] == "lan" || pin.isNotEmpty()) "lan" else "remote"
    return PairingRequest(origin = origin, code = code, certPin = pin, hostName = hostName, mode = mode)
}
