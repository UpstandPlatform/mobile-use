package org.mobileuse.companion

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
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
import android.os.SystemClock
import android.view.WindowManager
import java.io.ByteArrayOutputStream
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.MutableStateFlow

object Sharing {
    val session = MutableStateFlow<String?>(null)
}

class ScreenShareService : Service() {
    private val captureThread = HandlerThread("mobile-use-capture")
    private lateinit var captureHandler: Handler
    private val transfer = Executors.newSingleThreadExecutor()
    private val uploading = AtomicBoolean()
    private val checking = AtomicBoolean()
    private val closing = AtomicBoolean()
    private var projection: MediaProjection? = null
    private var display: VirtualDisplay? = null
    private var reader: ImageReader? = null
    private var sessionId: String? = null
    private var api: CompanionApi? = null
    private var lastFrame = 0L
    private var lastSuccess = 0L
    private val heartbeat = object : Runnable {
        override fun run() {
            if (closing.get()) return
            if (SystemClock.elapsedRealtime() - lastSuccess > 9500L) { stopSelf(); return }
            val id = sessionId
            val client = api
            if (id != null && client != null && checking.compareAndSet(false, true)) {
                transfer.submit {
                    try {
                        val current = client.heartbeat(id)
                        if (current.sessions.none { it.id == id && it.state == "active" }) stopSelf()
                        else captureHandler.post { lastSuccess = SystemClock.elapsedRealtime() }
                    } catch (error: ApiFailure) {
                        if (error.status in setOf(401, 403, 404, 409)) stopSelf()
                    } catch (_: Exception) {
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
        lastSuccess = SystemClock.elapsedRealtime()
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
            captureHandler.post { runCatching { configure(bounds.width(), bounds.height()) }.onFailure { stopSelf() } }
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
            if (now - lastSuccess > 9500L) { stopSelf(); return }
            if (now - lastFrame < 800L || !uploading.compareAndSet(false, true)) return
            lastFrame = now
            val id = sessionId
            val client = api
            if (id == null || client == null) { uploading.set(false); return }
            val bytes = runCatching {
                val plane = frame.planes[0]
                val bitmap = Bitmap.createBitmap(plane.rowStride / plane.pixelStride, frame.height, Bitmap.Config.ARGB_8888)
                try {
                    bitmap.copyPixelsFromBuffer(plane.buffer)
                    val crop = Bitmap.createBitmap(bitmap, 0, 0, frame.width, frame.height)
                    try { ByteArrayOutputStream().also { crop.compress(Bitmap.CompressFormat.JPEG, 73, it) }.toByteArray() }
                    finally { if (crop !== bitmap) crop.recycle() }
                } finally { bitmap.recycle() }
            }.getOrNull()
            if (bytes == null) { uploading.set(false); return }
            transfer.submit {
                try {
                    if (!closing.get()) {
                        client.frame(id, bytes)
                        captureHandler.post { lastSuccess = SystemClock.elapsedRealtime() }
                    }
                } catch (error: ApiFailure) {
                    if (error.status in setOf(401, 403, 404, 409)) stopSelf()
                } catch (_: Exception) {
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
        return Notification.Builder(this, CHANNEL).setContentTitle("Screen sharing is on")
            .setContentText("mobile-use is sharing the screen you selected.")
            .setSmallIcon(R.drawable.mobile_use_icon).setContentIntent(open).setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Stop", stop).build()).build()
    }

    override fun onDestroy() {
        closing.set(true)
        captureHandler.removeCallbacks(heartbeat)
        val id = sessionId
        val client = api
        captureHandler.post {
            if (id != null && client != null) transfer.submit { runCatching { client.session(id, "end") } }
            transfer.shutdown()
            display?.release()
            reader?.close()
            projection?.stop()
            captureThread.quitSafely()
        }
        Sharing.session.value = null
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val CHANNEL = "screen-sharing"
        private const val NOTIFICATION_ID = 100
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
