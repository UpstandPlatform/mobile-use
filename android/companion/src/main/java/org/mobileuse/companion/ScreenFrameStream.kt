package org.mobileuse.companion

import android.os.Handler
import android.os.Looper
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONObject
import java.util.concurrent.TimeUnit

internal enum class FrameStreamMode { CONNECTING, READY, LEGACY, CLOSED }

private const val MAX_QUEUED_FRAME_BYTES = 1_300_000L

internal fun screenFrameStreamUrl(origin: String, sessionId: String): String =
    origin.trimEnd('/').toHttpUrl().newBuilder()
        .addPathSegments("companion/sessions")
        .addPathSegment(sessionId)
        .addPathSegment("frames")
        .build()
        .toString()

/** A bounded, reconnecting WebSocket used for the attended screen-frame feed. */
internal class ScreenFrameStream(
    private val pairing: Pairing,
    private val sessionId: String,
    private val onFrame: () -> Unit,
    private val onError: (String) -> Unit,
    private val onFatal: (String) -> Unit,
    private val onLegacyServer: () -> Unit,
) {
    private val lock = Any()
    private val retryHandler = Handler(Looper.getMainLooper())
    private val client = companionSocketClient(pairing)
    @Volatile private var mode = FrameStreamMode.CONNECTING
    @Volatile private var closed = false
    private var socket: WebSocket? = null
    private var retryDelay = 300L
    private var retryTask: Runnable? = null

    fun start() = connect()

    fun mode(): FrameStreamMode = mode

    fun reconnect() {
        socket?.let(::reconnect)
    }

    fun send(bytes: ByteArray): Boolean {
        if (mode != FrameStreamMode.READY) return false
        val active = socket ?: return false
        if (active.queueSize() > MAX_QUEUED_FRAME_BYTES) {
            onError("The live screen connection fell behind. Reconnecting…")
            reconnect(active)
            return false
        }
        val accepted = active.send(ByteString.of(*bytes))
        if (!accepted) reconnect(active)
        return accepted
    }

    fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
            mode = FrameStreamMode.CLOSED
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
            if (closed || mode == FrameStreamMode.LEGACY) return
            retryTask = null
            mode = FrameStreamMode.CONNECTING
            // OkHttp's WebSocket handshake uses an HTTP(S) URL and upgrades the request itself.
            // HttpUrl.Builder only accepts http/https schemes; preserve the pinned HTTPS origin.
            val url = screenFrameStreamUrl(pairing.origin, sessionId)
            val request = Request.Builder().url(url)
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
                mode = FrameStreamMode.READY
                retryDelay = 300L
            }
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = runCatching { JSONObject(text) }.getOrNull() ?: return
            when (message.optString("type")) {
                "frame" -> onFrame()
                "dropped" -> Unit
                "error" -> {
                    val detail = message.optString("message").ifEmpty { "The server stopped the screen stream." }
                    synchronized(lock) { mode = FrameStreamMode.CLOSED; closed = true }
                    onFatal(detail)
                    webSocket.close(1000, "screen stream rejected")
                }
            }
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
            if (code == 1008) {
                synchronized(lock) { mode = FrameStreamMode.CLOSED; closed = true }
                onFatal(reason.ifEmpty { "The server no longer allows this screen-sharing session." })
            }
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            reconnect(webSocket)
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            val status = response?.code
            if (status == 404) {
                synchronized(lock) {
                    if (closed) return
                    mode = FrameStreamMode.LEGACY
                    socket = null
                }
                onLegacyServer()
                return
            }
            if (status == 401 || status == 403) {
                synchronized(lock) { mode = FrameStreamMode.CLOSED; closed = true }
                onFatal("The server rejected this screen-sharing session. End it and approve a new request.")
                return
            }
            onError(t.message ?: "The phone lost the live screen connection. Reconnecting…")
            reconnect(webSocket)
        }
    }

    private fun reconnect(previous: WebSocket) {
        synchronized(lock) {
            if (closed || mode == FrameStreamMode.LEGACY || socket !== previous) return
            socket = null
            mode = FrameStreamMode.CONNECTING
            // Cancel a backpressured socket before opening its replacement, so the server
            // handler and ping traffic do not remain active after this stream is abandoned.
            previous.cancel()
            if (retryTask != null) return
            val delay = retryDelay
            retryDelay = (retryDelay * 2).coerceAtMost(3000L)
            retryTask = Runnable { connect() }.also { retryHandler.postDelayed(it, delay) }
        }
    }

}

internal fun companionSocketClient(pairing: Pairing): OkHttpClient {
    val builder = OkHttpClient.Builder()
        .connectTimeout(4, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .writeTimeout(4, TimeUnit.SECONDS)
        .pingInterval(5, TimeUnit.SECONDS)
        .followRedirects(false)
    pairing.certPin.takeIf { it.isNotEmpty() }?.let { pin ->
        val pinned = buildPinnedSsl(pin)
        builder.sslSocketFactory(pinned.context.socketFactory, pinned.trustManager)
            .hostnameVerifier(pinned.hostnameVerifier)
    }
    return builder.build()
}
