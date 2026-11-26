package org.mobileuse.companion

import android.os.Handler
import android.os.Looper
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/** Small authenticated control channel dedicated to visual gesture events. */
internal class GestureEventStream(
    private val pairing: Pairing,
    private val sessionId: String,
    private val onGesture: (PointerGesture) -> Unit,
    private val onFatal: (String) -> Unit,
) {
    private val lock = Any()
    private val retryHandler = Handler(Looper.getMainLooper())
    private val client = buildClient()
    @Volatile private var closed = false
    private var socket: WebSocket? = null
    private var retryTask: Runnable? = null
    private var retryDelay = 300L

    fun start() = connect()

    fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
            retryTask?.let(retryHandler::removeCallbacks)
            retryTask = null
            socket?.close(1000, "screen sharing stopped")
            socket = null
        }
        client.dispatcher.cancelAll()
        client.connectionPool.evictAll()
        client.dispatcher.executorService.shutdown()
    }

    private fun connect() {
        synchronized(lock) {
            if (closed) return
            retryTask = null
            val request = Request.Builder()
                .url(gestureStreamUrl(pairing.origin, sessionId))
                .header("Authorization", "Bearer ${pairing.token}")
                .build()
            socket = client.newWebSocket(request, listener)
        }
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            synchronized(lock) {
                if (closed) {
                    webSocket.close(1000, "screen sharing stopped")
                    return
                }
                socket = webSocket
                retryDelay = 300L
            }
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = runCatching { JSONObject(text) }.getOrNull() ?: return
            if (message.optString("type") == "gesture") {
                parsePointerGesture(message)?.let(onGesture)
            } else if (message.optString("type") == "error") {
                val detail = message.optString("message").ifEmpty { "The server ended the gesture pointer connection." }
                synchronized(lock) { closed = true }
                onFatal(detail)
                webSocket.close(1000, "gesture stream rejected")
            }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
            if (code == 1008) {
                synchronized(lock) { closed = true }
                onFatal(reason.ifEmpty { "The server no longer allows this screen-sharing session." })
            }
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = reconnect(webSocket)

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            if (response?.code in setOf(401, 403)) {
                synchronized(lock) { closed = true }
                onFatal("The server rejected the gesture pointer connection. End this session and approve a new request.")
            } else {
                reconnect(webSocket)
            }
        }
    }

    private fun reconnect(previous: WebSocket) {
        synchronized(lock) {
            if (closed || socket !== previous) return
            socket = null
            previous.cancel()
            if (retryTask != null) return
            val delay = retryDelay
            retryDelay = (retryDelay * 2).coerceAtMost(3000L)
            retryTask = Runnable { connect() }.also { retryHandler.postDelayed(it, delay) }
        }
    }

    private fun buildClient(): OkHttpClient {
        val builder = OkHttpClient.Builder()
            .connectTimeout(4, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .writeTimeout(4, TimeUnit.SECONDS)
            .pingInterval(5, TimeUnit.SECONDS)
        pairing.certPin.takeIf { it.isNotEmpty() }?.let { pin ->
            val pinned = buildPinnedSsl(pin)
            builder.sslSocketFactory(pinned.context.socketFactory, pinned.trustManager)
                .hostnameVerifier(pinned.hostnameVerifier)
        }
        return builder.build()
    }
}

private fun gestureStreamUrl(origin: String, sessionId: String): String =
    origin.trimEnd('/').toHttpUrl().newBuilder()
        .addPathSegments("companion/sessions")
        .addPathSegment(sessionId)
        .addPathSegment("gestures")
        .build()
        .toString()
