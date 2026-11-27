package org.mobileuse.companion

import android.content.Context
import android.os.Handler
import android.os.Looper
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** Authenticated request/response channel for one owner-approved phone session. */
internal class ControlCommandStream(
    private val context: Context,
    private val pairing: Pairing,
    private val sessionId: String,
    private val onFatal: () -> Unit,
) {
    private val lock = Any()
    private val retryHandler = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private val client = buildClient()
    @Volatile private var closed = false
    private var socket: WebSocket? = null
    private var retryTask: Runnable? = null
    private var retryDelay = 300L

    fun start() = connect()

    fun close() {
        synchronized(lock) {
            closed = true
            retryTask?.let(retryHandler::removeCallbacks)
            retryTask = null
            socket?.close(1000, "control session stopped")
            socket = null
        }
        executor.shutdownNow()
        client.dispatcher.cancelAll()
        client.connectionPool.evictAll()
        client.dispatcher.executorService.shutdown()
    }

    private fun connect() {
        synchronized(lock) {
            if (closed) return
            retryTask = null
            val request = Request.Builder()
                .url(controlStreamUrl(pairing.origin, sessionId))
                .header("Authorization", "Bearer ${pairing.token}")
                .build()
            socket = client.newWebSocket(request, listener)
        }
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            synchronized(lock) {
                if (closed) {
                    webSocket.close(1000, "control session stopped")
                    return
                }
                socket = webSocket
                retryDelay = 300L
            }
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = runCatching { JSONObject(text) }.getOrNull() ?: return
            if (message.optString("type") != "command") return
            val commandId = message.optString("id")
            val dispatch = message.optJSONObject("dispatch")
            if (!commandId.matches(Regex("[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,127}")) || dispatch == null ||
                dispatch.optString("session_id") != sessionId || dispatch.optJSONObject("operation")?.optString("id") != commandId
            ) {
                reject(webSocket, commandId, "INVALID_COMMAND", "The control request did not match the approved session.")
                return
            }
            try {
                executor.execute {
                    if (closed) return@execute
                    val result = PhoneControl.execute(context, dispatch)
                    webSocket.send(JSONObject().put("type", "result").put("id", commandId).put("result", result).toString())
                }
            } catch (_: Exception) {
                reject(webSocket, commandId, "CONTROL_QUEUE_FULL", "The phone is busy with another approved action.")
            }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = reconnect(webSocket)

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            if (response?.code in setOf(401, 403, 404)) fail() else reconnect(webSocket)
        }
    }

    private fun reject(webSocket: WebSocket, id: String, code: String, detail: String) {
        val result = JSONObject().put("id", id).put("state", "failed")
            .put("error", JSONObject().put("code", code).put("message", detail).put("status", 409))
        webSocket.send(JSONObject().put("type", "result").put("id", id).put("result", result).toString())
    }

    private fun fail() {
        synchronized(lock) {
            if (closed) return
            closed = true
        }
        onFatal()
    }

    private fun reconnect(previous: WebSocket) {
        synchronized(lock) {
            if (closed || socket !== previous) return
            socket = null
            previous.cancel()
            if (retryTask != null) return
            val delay = retryDelay
            retryDelay = (retryDelay * 2).coerceAtMost(5000L)
            retryTask = Runnable { connect() }.also { retryHandler.postDelayed(it, delay) }
        }
    }

    private fun buildClient(): OkHttpClient {
        val builder = OkHttpClient.Builder()
            .connectTimeout(4, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .writeTimeout(5, TimeUnit.SECONDS)
            .pingInterval(5, TimeUnit.SECONDS)
        pairing.certPin.takeIf { it.isNotEmpty() }?.let { pin ->
            val pinned = buildPinnedSsl(pin)
            builder.sslSocketFactory(pinned.context.socketFactory, pinned.trustManager)
                .hostnameVerifier(pinned.hostnameVerifier)
        }
        return builder.build()
    }
}

internal fun controlStreamUrl(origin: String, sessionId: String): String {
    val url = origin.trimEnd('/').toHttpUrl()
    return url.newBuilder()
        .addPathSegments("companion/sessions")
        .addPathSegment(sessionId)
        .addPathSegment("control")
        .build()
        .toString()
}
