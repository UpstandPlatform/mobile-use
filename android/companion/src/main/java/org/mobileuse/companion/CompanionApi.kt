package org.mobileuse.companion

import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.URL
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLHandshakeException
import javax.net.ssl.X509TrustManager
import org.json.JSONArray
import org.json.JSONObject

data class PhoneSession(val id: String, val purpose: String, val requester: String, val state: String, val caps: Set<String>)
data class SensorRequest(val id: String, val sessionId: String, val type: String, val seconds: Int)
data class Heartbeat(val sessions: List<PhoneSession>, val sensors: List<SensorRequest>)
class ApiFailure(val status: Int, message: String) : IOException(message)

class CompanionApi(private val pairing: Pairing) {
    private val pinnedSsl = pairing.certPin.takeIf { it.isNotEmpty() }?.let(::buildPinnedSsl)

    fun heartbeat(present: String?): Heartbeat {
        val result = json("/companion/heartbeat", JSONObject().put("present_session", present.orEmpty()))
        val sessions = result.optJSONArray("sessions").objects().map {
            val caps = it.optJSONArray("capabilities") ?: JSONArray()
            PhoneSession(it.getString("id"), it.getString("purpose"), it.getString("requester"), it.getString("state"),
                (0 until caps.length()).map(caps::getString).toSet())
        }
        val sensors = result.optJSONArray("sensors").objects().map {
            SensorRequest(it.getString("id"), it.getString("session_id"), it.getString("type"), it.optInt("duration_seconds", 5))
        }
        return Heartbeat(sessions, sensors)
    }

    fun session(id: String, action: String) { json("/companion/sessions/${safeId(id)}/$action", JSONObject()) }
    fun denySensor(id: String) { json("/companion/sensors/${safeId(id)}/deny", JSONObject()) }
    fun upload(id: String, bytes: ByteArray, mime: String) { post("/companion/sensors/${safeId(id)}/artifact", mime, bytes) }
    fun frame(id: String, bytes: ByteArray) { post("/companion/sessions/${safeId(id)}/frame", "image/jpeg", bytes) }
    fun unpair() { json("/companion/unpair", JSONObject()) }

    private fun json(path: String, value: JSONObject): JSONObject =
        JSONObject(post(path, "application/json", value.toString().toByteArray()))

    private fun post(path: String, mime: String, data: ByteArray): String {
        val connection = URL(pairing.origin + path).openConnection() as HttpsURLConnection
        try {
            if (pinnedSsl != null) {
                connection.sslSocketFactory = pinnedSsl.first.socketFactory
                connection.hostnameVerifier = pinnedSsl.second
            }
            connection.requestMethod = "POST"
            connection.instanceFollowRedirects = false
            connection.connectTimeout = 4000
            connection.readTimeout = 6000
            connection.setRequestProperty("Content-Type", mime)
            if (pairing.token.isNotEmpty()) connection.setRequestProperty("Authorization", "Bearer ${pairing.token}")
            connection.doOutput = true
            connection.setFixedLengthStreamingMode(data.size)
            connection.outputStream.use { it.write(data) }
            val status = connection.responseCode
            val body = if (status == HttpsURLConnection.HTTP_NO_CONTENT) ""
                else (if (status in 200..299) connection.inputStream else connection.errorStream)
                    ?.use { input -> input.readNBytes(128 * 1024).toString(Charsets.UTF_8) }.orEmpty()
            if (status !in 200..299) {
                val message = runCatching { JSONObject(body).getJSONObject("error").getString("message") }.getOrNull()
                throw ApiFailure(status, message ?: "The server rejected the request ($status).")
            }
            return body
        } catch (e: SSLHandshakeException) {
            throw ApiFailure(0, "Certificate check failed. Scan a fresh QR code or verify your HTTPS certificate.")
        } catch (e: ConnectException) {
            throw ApiFailure(0, "Could not reach ${pairing.origin}. Confirm both devices are on the same Wi-Fi.")
        } catch (e: SocketTimeoutException) {
            throw ApiFailure(0, "Connection timed out reaching ${pairing.origin}. Check Wi-Fi and firewall rules.")
        } finally {
            connection.disconnect()
        }
    }

    companion object {
        fun enroll(
            origin: String,
            code: String,
            label: String,
            certPin: String = "",
            hostName: String = "",
            mode: String = if (certPin.isNotEmpty()) "lan" else "remote",
        ): Pairing {
            val address = normalizedOrigin(origin)
            val normalizedCode = normalizePairingCode(code)
            val normalizedPin = normalizeCertPin(certPin)
            val value = CompanionApi(Pairing(origin = address, token = "", certPin = normalizedPin, hostName = hostName, mode = mode))
                .json("/companion/enroll", JSONObject().put("code", normalizedCode).put("label", label.take(80)))
            return Pairing(
                origin = address,
                token = value.getString("token"),
                certPin = normalizedPin,
                hostName = hostName,
                mode = mode,
            )
        }
    }
}

private fun buildPinnedSsl(hexPin: String): Pair<SSLContext, HostnameVerifier> {
    val expected = hexToBytes(hexPin)
    val trustManager = object : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            throw CertificateException("Client certificates not supported")
        }

        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            if (chain.isNullOrEmpty()) throw CertificateException("Empty server certificate chain")
            chain[0].checkValidity()
            val digest = MessageDigest.getInstance("SHA-256").digest(chain[0].encoded)
            if (!MessageDigest.isEqual(digest, expected)) {
                throw CertificateException("Server certificate fingerprint mismatch")
            }
        }

        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }
    val context = SSLContext.getInstance("TLS")
    context.init(null, arrayOf(trustManager), null)
    val verifier = HostnameVerifier { _, session ->
        val leaf = runCatching { session.peerCertificates.firstOrNull() as? X509Certificate }.getOrNull()
            ?: return@HostnameVerifier false
        val digest = MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
        MessageDigest.isEqual(digest, expected)
    }
    return context to verifier
}

private fun hexToBytes(hex: String): ByteArray {
    val out = ByteArray(hex.length / 2)
    for (i in out.indices) {
        out[i] = hex.substring(i * 2, i * 2 + 2).toInt(16).toByte()
    }
    return out
}

private fun safeId(value: String): String {
    require(value.matches(Regex("[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,127}")))
    return value
}

private fun JSONArray?.objects(): List<JSONObject> = if (this == null) emptyList() else (0 until length()).map { getJSONObject(it) }
