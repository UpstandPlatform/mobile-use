package org.mobileuse.companion

import android.os.Handler
import android.os.SystemClock
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.Buffer
import okio.ByteString

internal enum class MediaSource(val id: String, val label: String) {
    SCREEN("screen", "Screen"), CAMERA("camera", "Camera"), MICROPHONE("microphone", "Microphone")
}

/** Keeps the publisher close to the live edge even when a TCP connection accepts stale writes. */
internal class LiveMediaSocket(
    pairing: Pairing,
    session: String,
    private val source: MediaSource,
    private val handler: Handler,
    private val onReady: () -> Unit,
    private val onSample: () -> Unit,
    private val onUnavailable: () -> Unit,
    private val onFatal: (String) -> Unit,
) : AutoCloseable {
    private val client = companionSocketClient(pairing)
    private val request = Request.Builder()
        .url(pairing.origin.toHttpUrl().newBuilder().addPathSegments("companion/sessions")
            .addPathSegment(session).addPathSegment("media").addPathSegment(source.id).build())
        .header("Authorization", "Bearer ${pairing.token}").build()
    private val lock = Any()
    private val pending = ArrayDeque<Pair<Long, Long>>()
    private var socket: WebSocket? = null
    private var retryDelay = 250L
    private var closed = false
    private var needKeyFrame = true
    @Volatile private var ready = false
    @Volatile var previewRequested = false
        private set
    private val retry = Runnable { start() }

    fun start() = synchronized(lock) {
        if (!closed && socket == null) socket = client.newWebSocket(request, listener)
    }

    fun send(pts: Long, data: ByteArray, keyFrame: Boolean = false) = synchronized(lock) {
        val active = socket ?: return
        if (!ready) return
        val now = SystemClock.elapsedRealtime()
        val oldest = pending.firstOrNull()?.second
        if (oldest != null && now - oldest > 900 || active.queueSize() > 96 * 1024) {
            reconnect(active)
            return
        }
        if (source != MediaSource.MICROPHONE && needKeyFrame && !keyFrame) return
        val message = Buffer().writeLong(pts).write(data).readByteString()
        if (active.send(message)) {
            needKeyFrame = false
            pending.addLast(pts to now)
        } else reconnect(active)
    }

    override fun close() {
        synchronized(lock) {
            closed = true
            ready = false
            handler.removeCallbacks(retry)
            socket?.cancel()
            socket = null
            pending.clear()
        }
        client.dispatcher.cancelAll()
        client.connectionPool.evictAll()
        client.dispatcher.executorService.shutdown()
    }

    private fun reconnect(active: WebSocket) {
        if (closed || socket !== active) return
        socket = null
        ready = false
        active.cancel()
        pending.clear()
        handler.postDelayed(retry, retryDelay)
        retryDelay = (2 * retryDelay).coerceAtMost(3000L)
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) = synchronized(lock) {
            if (closed || socket !== webSocket) return
            ready = true
            needKeyFrame = true
            handler.post(onReady)
            Unit
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) = synchronized(lock) {
            if (!ready || socket !== webSocket || bytes.size != 9) return
            val ack = Buffer().write(bytes)
            val pts = ack.readLong()
            if (pending.firstOrNull()?.first != pts) return
            pending.removeFirst()
            retryDelay = 250L
            previewRequested = ack.readByte().toInt() == 1
            handler.post(onSample)
            Unit
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) = synchronized(lock) {
            if (closed || socket !== webSocket) return
            if (code == 1003) {
                ready = false
                handler.post { onFatal(reason) }
            } else reconnect(webSocket)
            Unit
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = synchronized(lock) {
            if (closed || socket !== webSocket) return
            when (response?.code) {
                404, 405, 501 -> handler.post(onUnavailable)
                401, 403 -> handler.post { onFatal("The server no longer allows ${source.label.lowercase()} sharing for this session.") }
                else -> { reconnect(webSocket); return }
            }
            ready = false
        }
    }
}
