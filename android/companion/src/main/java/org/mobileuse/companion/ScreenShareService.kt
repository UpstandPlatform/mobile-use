package org.mobileuse.companion

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.BroadcastReceiver
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.graphics.Bitmap
import android.graphics.PixelFormat
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.ImageReader
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.PowerManager
import android.os.SystemClock
import android.view.WindowManager
import java.io.ByteArrayOutputStream
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.MutableStateFlow

object Sharing {
    val session = MutableStateFlow<String?>(null)
    val error = MutableStateFlow<String?>(null)
    val lastFrameAt = MutableStateFlow<Long?>(null)
}

class ScreenShareService : Service() {
    private val captureThread = HandlerThread("mobile-use-capture")
    private lateinit var captureHandler: Handler
    private val transfer = Executors.newSingleThreadExecutor()
    private val control = Executors.newSingleThreadExecutor()
    private val uploading = AtomicBoolean()
    private val checking = AtomicBoolean()
    private val closing = AtomicBoolean()
    private var projection: MediaProjection? = null
    private var display: VirtualDisplay? = null
    private var reader: ImageReader? = null
    private var sessionId: String? = null
    private var api: CompanionApi? = null
    private var frameStream: ScreenFrameStream? = null
    private var lastFrame = 0L
    @Volatile
    private var lastSuccess = 0L
    private var startedAt = 0L
    private var frameIntervalMs = REMOTE_FRAME_INTERVAL_MS
    private val displayStateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (intent?.action == Intent.ACTION_SCREEN_OFF || intent?.action == Intent.ACTION_SCREEN_ON) {
                getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification())
            }
        }
    }
    private val heartbeat = object : Runnable {
        override fun run() {
            if (closing.get()) return
            val now = SystemClock.elapsedRealtime()
            if (now - lastSuccess > 9500L) {
                reportError("The phone is reconnecting to the server. Screen sharing will resume when Wi-Fi is back.")
            }
            val frameAt = Sharing.lastFrameAt.value
            if (frameAt == null && now - startedAt > 10000L) {
                reportError("Android has not produced a screen frame yet. Stop sharing and approve the screen prompt again.")
            } else if (frameAt != null && now - frameAt > 10000L) {
                reportError("Screen frames stopped reaching the server. Check Wi-Fi, then restart screen sharing.")
            } else if (frameAt != null && now - frameAt > FRAME_STALE_MS && frameStream?.mode() == FrameStreamMode.READY) {
                reportError("The live screen stream stalled. Reconnecting to the server…")
                frameStream?.reconnect()
            }
            val id = sessionId
            val client = api
            if (id != null && client != null && checking.compareAndSet(false, true)) {
                control.submit {
                    try {
                        val current = client.heartbeat(id)
                        CompanionNotifications.syncRequests(this@ScreenShareService, current)
                        CompanionNotifications.connectionChanged(this@ScreenShareService, true)
                        if (current.sessions.none { it.id == id && it.state == "active" }) stopSelf()
                        else captureHandler.post { lastSuccess = SystemClock.elapsedRealtime() }
                    } catch (error: ApiFailure) {
                        CompanionNotifications.connectionChanged(this@ScreenShareService, false, error.message)
                        reportError(error.message)
                        if (error.status in setOf(401, 403, 404, 409)) stopSelf()
                    } catch (error: Exception) {
                        CompanionNotifications.connectionChanged(this@ScreenShareService, false)
                        reportError(error.message ?: "The phone could not check its screen-sharing session.")
                        // The watchdog stops projection after losing backend presence.
                    } finally { checking.set(false) }
                }
            }
            captureHandler.postDelayed(this, 2500)
        }
    }

    override fun onCreate() {
        super.onCreate()
        captureThread.start()
        captureHandler = Handler(captureThread.looper)
        registerReceiver(displayStateReceiver, IntentFilter().apply {
            addAction(Intent.ACTION_SCREEN_OFF)
            addAction(Intent.ACTION_SCREEN_ON)
        }, Context.RECEIVER_NOT_EXPORTED)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (sessionId != null) return START_NOT_STICKY
        val id = intent?.getStringExtra(SESSION)
        val permission = intent?.getParcelableExtra(PERMISSION, Intent::class.java)
        val pairing = PairingStore(this).read()
        if (id == null || permission == null || pairing == null) {
            // start() uses startForegroundService(), so the service must still enter the
            // foreground before stopping or the system raises ForegroundServiceDidNotStartInTime.
            // A media-projection type can be refused without a consent token; stop either way.
            runCatching {
                startForeground(NOTIFICATION_ID, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION)
            }
            stopSelf()
            return START_NOT_STICKY
        }
        startForeground(NOTIFICATION_ID, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION)
        sessionId = id
        api = CompanionApi(pairing)
        frameIntervalMs = if (pairing.mode == "lan") LAN_FRAME_INTERVAL_MS else REMOTE_FRAME_INTERVAL_MS
        frameStream = ScreenFrameStream(
            pairing = pairing,
            sessionId = id,
            onFrame = {
                if (!closing.get()) {
                    val uploadedAt = SystemClock.elapsedRealtime()
                    lastSuccess = uploadedAt
                    Sharing.lastFrameAt.value = uploadedAt
                    reportError(null)
                }
            },
            onError = { reportError(it) },
            onFatal = {
                reportError(it)
                stopSelf()
            },
            onLegacyServer = { reportError(null) },
        ).also { it.start() }
        lastSuccess = SystemClock.elapsedRealtime()
        startedAt = lastSuccess
        reportError(null)
        Sharing.lastFrameAt.value = null
        try {
            val manager = getSystemService(MediaProjectionManager::class.java)
            projection = manager.getMediaProjection(intent.getIntExtra(RESULT, 0), permission)
            projection?.registerCallback(object : MediaProjection.Callback() {
                override fun onStop() { stopSelf() }
                override fun onCapturedContentResize(width: Int, height: Int) {
                    if (!closing.get() && display != null) configure(width, height)
                }
            }, captureHandler)
            val bounds = getSystemService(WindowManager::class.java).maximumWindowMetrics.bounds
            captureHandler.post {
                runCatching { configure(bounds.width(), bounds.height()) }.onFailure {
                    reportError("Android could not start screen capture. Stop sharing and approve the screen prompt again.")
                    stopSelf()
                }
            }
            captureHandler.postDelayed(heartbeat, 2000)
            Sharing.session.value = id
        } catch (_: RuntimeException) {
            stopSelf()
        }
        return START_NOT_STICKY
    }

    private fun configure(width: Int, height: Int) {
        if (width < 1 || height < 1 || closing.get()) return
        val source = projection ?: return
        val scale = minOf(1f, 960f / maxOf(width, height))
        val outputWidth = (width * scale).roundToInt()
        val outputHeight = (height * scale).roundToInt()
        val density = resources.configuration.densityDpi
        val next = ImageReader.newInstance(outputWidth, outputHeight, PixelFormat.RGBA_8888, 2)
        next.setOnImageAvailableListener(::frameAvailable, captureHandler)
        val old = reader
        reader = next
        val existing = display
        if (existing == null) {
            display = source.createVirtualDisplay("mobile-use screen sharing", outputWidth, outputHeight, density,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR, next.surface, null, captureHandler)
        } else {
            existing.resize(outputWidth, outputHeight, density)
            existing.surface = next.surface
        }
        old?.close()
    }

    private fun frameAvailable(source: ImageReader) {
        if (closing.get()) return
        val image = runCatching { source.acquireLatestImage() }.getOrNull() ?: return
        image.use { frame ->
            val now = SystemClock.elapsedRealtime()
            if (now - lastFrame < frameIntervalMs || !uploading.compareAndSet(false, true)) return
            lastFrame = now
            val id = sessionId
            val client = api
            val stream = frameStream
            if (id == null || client == null || stream == null) { uploading.set(false); return }
            val streamMode = stream.mode()
            if (streamMode == FrameStreamMode.CONNECTING || streamMode == FrameStreamMode.CLOSED) {
                uploading.set(false)
                return
            }
            val bytes = runCatching {
                val plane = frame.planes[0]
                val bitmap = Bitmap.createBitmap(plane.rowStride / plane.pixelStride, frame.height, Bitmap.Config.ARGB_8888)
                try {
                    bitmap.copyPixelsFromBuffer(plane.buffer)
                    val crop = Bitmap.createBitmap(bitmap, 0, 0, frame.width, frame.height)
                    try { ByteArrayOutputStream().also { crop.compress(Bitmap.CompressFormat.JPEG, 68, it) }.toByteArray() }
                    finally { if (crop !== bitmap) crop.recycle() }
                } finally { bitmap.recycle() }
            }.getOrNull()
            if (bytes == null) {
                reportError("The phone could not encode a screen frame. Restart screen sharing and try again.")
                uploading.set(false)
                return
            }
            if (streamMode == FrameStreamMode.READY) {
                stream.send(bytes)
                uploading.set(false)
                return
            }
            transfer.submit {
                try {
                    if (!closing.get()) {
                        client.frame(id, bytes)
                        captureHandler.post {
                            val uploadedAt = SystemClock.elapsedRealtime()
                            lastSuccess = uploadedAt
                            Sharing.lastFrameAt.value = uploadedAt
                            reportError(null)
                        }
                    }
                } catch (error: ApiFailure) {
                    if (error.status != 429) reportError(error.message)
                    if (error.status in setOf(401, 403, 404, 409)) stopSelf()
                } catch (error: Exception) {
                    reportError(error.message ?: "The phone could not send a screen frame. Check Wi-Fi and the server address.")
                    // The capture watchdog stops sharing if the server remains unreachable.
                } finally { uploading.set(false) }
            }
        }
    }

    private fun notification(): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel(CHANNEL, "Screen sharing", NotificationManager.IMPORTANCE_LOW))
        val stop = PendingIntent.getService(this, 1, Intent(this, ScreenShareService::class.java).setAction(STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val open = PendingIntent.getActivity(this, 2, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val screenIsOn = getSystemService(PowerManager::class.java).isInteractive
        val details = if (screenIsOn) "mobile-use is sharing the screen you selected."
            else "Sharing continues, but Android may return blank frames while the screen is locked or off."
        return Notification.Builder(this, CHANNEL).setContentTitle("Screen sharing is on")
            .setContentText(details)
            .setSmallIcon(R.drawable.ic_notification).setContentIntent(open)
            .setVisibility(Notification.VISIBILITY_PRIVATE).setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Stop", stop).build()).build()
    }

    private fun reportError(message: String?) {
        Sharing.error.value = message
        if (message.isNullOrBlank()) CompanionNotifications.clearScreenShareError(this)
        else CompanionNotifications.screenShareError(this, message)
    }

    override fun onDestroy() {
        closing.set(true)
        runCatching { unregisterReceiver(displayStateReceiver) }
        captureHandler.removeCallbacks(heartbeat)
        frameStream?.close()
        val id = sessionId
        val client = api
        captureHandler.post {
            if (id != null && client != null) transfer.submit { runCatching { client.session(id, "end") } }
            transfer.shutdown()
            control.shutdown()
            display?.release()
            reader?.close()
            projection?.stop()
            captureThread.quitSafely()
        }
        Sharing.session.value = null
        Sharing.lastFrameAt.value = null
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val CHANNEL = "screen-sharing"
        private const val NOTIFICATION_ID = 100
        private const val LAN_FRAME_INTERVAL_MS = 125L
        private const val REMOTE_FRAME_INTERVAL_MS = 200L
        private const val FRAME_STALE_MS = 3500L
        private const val STOP = "org.mobileuse.companion.STOP"
        private const val SESSION = "session"
        private const val PERMISSION = "permission"
        private const val RESULT = "result"

        fun start(context: Context, sessionId: String, result: Int, permission: Intent) {
            context.startForegroundService(Intent(context, ScreenShareService::class.java)
                .putExtra(SESSION, sessionId).putExtra(RESULT, result).putExtra(PERMISSION, permission))
        }

        fun stop(context: Context) { context.stopService(Intent(context, ScreenShareService::class.java)) }
    }
}
