package org.mobileuse.companion

import java.io.IOException
import java.net.URL
import javax.net.ssl.HttpsURLConnection
import org.json.JSONArray
import org.json.JSONObject

data class PhoneSession(val id: String, val purpose: String, val requester: String, val state: String, val caps: Set<String>)
data class SensorRequest(val id: String, val sessionId: String, val type: String, val seconds: Int)
data class Heartbeat(val sessions: List<PhoneSession>, val sensors: List<SensorRequest>)
class ApiFailure(val status: Int, message: String) : IOException(message)

class CompanionApi(private val pairing: Pairing) {
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
            connection.requestMethod = "POST"
            connection.instanceFollowRedirects = false
            connection.connectTimeout = 3500
            connection.readTimeout = 5000
            connection.setRequestProperty("Content-Type", mime)
            if (pairing.token.isNotEmpty()) connection.setRequestProperty("Authorization", "Bearer ${pairing.token}")
            connection.doOutput = true
            connection.setFixedLengthStreamingMode(data.size)
            connection.outputStream.use { it.write(data) }
            val status = connection.responseCode
            val stream = if (status in 200..299) connection.inputStream else connection.errorStream
            val body = stream?.use { input -> input.readNBytes(128 * 1024).toString(Charsets.UTF_8) }.orEmpty()
            if (status !in 200..299) {
                val message = runCatching { JSONObject(body).getJSONObject("error").getString("message") }.getOrNull()
                throw ApiFailure(status, message ?: "The server rejected the request ($status).")
            }
            return body
        } finally {
            connection.disconnect()
        }
    }

    companion object {
        fun enroll(origin: String, code: String, label: String): Pairing {
            val address = normalizedOrigin(origin)
            require(code.matches(Regex("[a-fA-F0-9]{32}"))) { "Enter a valid pairing code." }
            val value = CompanionApi(Pairing(address, "")).json("/companion/enroll",
                JSONObject().put("code", code.lowercase()).put("label", label.take(80)))
            return Pairing(address, value.getString("token"))
        }
    }
}

private fun safeId(value: String): String {
    require(value.matches(Regex("[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,127}")))
    return value
}

private fun JSONArray?.objects(): List<JSONObject> = if (this == null) emptyList() else (0 until length()).map { getJSONObject(it) }
