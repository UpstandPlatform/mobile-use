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
import android.provider.Settings
import android.view.PixelCopy
import android.view.Surface
import android.view.WindowManager
import java.io.ByteArrayOutputStream
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.MutableStateFlow

object Sharing {
    val session = MutableStateFlow<String?>(null)
    val error = MutableStateFlow<String?>(null)
    val hasFrames = MutableStateFlow(false)
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
    private var encoder: AvcEncoder? = null
    private var sessionId: String? = null
    private var api: CompanionApi? = null
    private var activePairing: Pairing? = null
    private var frameStream: ScreenFrameStream? = null
    private var gestureStream: GestureEventStream? = null
    private var pointerOverlay: GesturePointerOverlay? = null
    private var mediaStream: LiveMediaSocket? = null
    private var allowVideo = true
    private var bitRate = 900_000
    private var lastFrame = 0L
    private var lastCapturedAt = 0L
    private var lastDeliveredAt: Long? = null
    @Volatile
    private var lastSuccess = 0L
    private var startedAt = 0L
    private var firstFrameRecoveryAttempted = false
    private var sourceWidth = 0
    private var sourceHeight = 0
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
            val frameAt = lastDeliveredAt
            val captureAge = now - startedAt
            if (lastCapturedAt == 0L && captureAge > FIRST_FRAME_RECOVERY_MS && !firstFrameRecoveryAttempted) {
                firstFrameRecoveryAttempted = true
                reportError("Screen capture is taking longer than expected. Restarting the capture surface…")
                if (sourceWidth > 0 && sourceHeight > 0) {
                    runCatching { configure(sourceWidth, sourceHeight) }.onFailure {
                        reportError("Android could not restart the screen capture surface. Reopen Share screen and approve the prompt again.")
                    }
                }
            } else if (lastCapturedAt == 0L && captureAge > FIRST_FRAME_TIMEOUT_MS) {
                reportError("Android has not produced a screen frame. Check that you selected the whole screen in Android’s capture prompt, then stop and restart sharing.")
            } else if (frameAt == null && lastCapturedAt > 0L && captureAge > FRAME_ACK_TIMEOUT_MS) {
                reportError("Android is capturing the screen, but the server has not confirmed a frame. Check the phone’s Wi-Fi and server connection.")
            } else if (frameAt != null && now - frameAt > 10000L) {
                reportError("Screen frames stopped reaching the server. Check Wi-Fi, then restart screen sharing.")
            } else if (encoder == null && frameAt != null && now - frameAt > FRAME_STALE_MS && frameStream?.mode() == FrameStreamMode.READY) {
                reportError("The live screen stream stalled. Reconnecting to the server…")
                frameStream?.reconnect()
            }
            val id = sessionId
            val client = api
            if (id != null && client != null && checking.compareAndSet(false, true)) {
                control.submit {
                    try {
                        val current = client.heartbeat(id, PhoneControlCapabilities.current(this@ScreenShareService))
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
        if (intent?.action == UPDATE_POINTER_OVERLAY) {
            updatePointerState()
            if (sessionId == null) stopSelf()
            return START_NOT_STICKY
        }
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
        activePairing = pairing
        api = CompanionApi(pairing)
        if (GesturePointerSettings.enabled(this)) {
            control.submit { runCatching { api?.gesturePointer(true) } }
        }
        frameIntervalMs = if (pairing.mode == "lan") LAN_FRAME_INTERVAL_MS else REMOTE_FRAME_INTERVAL_MS
        bitRate = if (pairing.mode == "lan") 1_500_000 else 900_000
        frameStream = ScreenFrameStream(
            pairing = pairing,
            sessionId = id,
            onFrame = { captureHandler.post(::frameDelivered) },
            onError = { reportError(it) },
            onFatal = {
                reportError(it)
                stopSelf()
            },
            onLegacyServer = { reportError(null) },
        ).also { it.start() }
        mediaStream = LiveMediaSocket(pairing, id, MediaSource.SCREEN, captureHandler,
            onReady = { encoder?.requestKeyFrame() }, onSample = ::frameDelivered,
            onUnavailable = ::useJpegCapture, onFatal = { useJpegCapture() },
        ).also { it.start() }
        updatePointerState()
        lastSuccess = SystemClock.elapsedRealtime()
        startedAt = lastSuccess
        lastCapturedAt = 0L
        lastFrame = 0L
        firstFrameRecoveryAttempted = false
        sourceWidth = 0
        sourceHeight = 0
        reportError(null)
        Sharing.hasFrames.value = false
        try {
            val manager = getSystemService(MediaProjectionManager::class.java)
            projection = manager.getMediaProjection(intent.getIntExtra(RESULT, 0), permission)
                ?: throw IllegalStateException("Android did not return a screen projection.")
            projection?.registerCallback(object : MediaProjection.Callback() {
                override fun onStop() { stopSelf() }
                override fun onCapturedContentResize(width: Int, height: Int) {
                    if (closing.get() || width < 1 || height < 1) return
                    if (width != sourceWidth || height != sourceHeight) configureCapture(width, height)
                }
            }, captureHandler)
            val bounds = getSystemService(WindowManager::class.java).maximumWindowMetrics.bounds
            captureHandler.post {
                val width = sourceWidth.takeIf { it > 0 } ?: bounds.width()
                val height = sourceHeight.takeIf { it > 0 } ?: bounds.height()
                if (display == null) configureCapture(width, height)
            }
            captureHandler.postDelayed(heartbeat, 2000)
            Sharing.session.value = id
        } catch (_: RuntimeException) {
            stopSelf()
        }
        return START_NOT_STICKY
    }

    private fun configureCapture(width: Int, height: Int) {
        runCatching { configure(width, height) }.onFailure {
            reportError("Android could not start the screen capture surface. Reopen Share screen and approve the prompt again.")
            stopSelf()
        }
    }

    private fun useJpegCapture() {
        if (closing.get()) return
        allowVideo = false
        mediaStream?.close()
        mediaStream = null
        if (encoder != null) configureCapture(sourceWidth, sourceHeight)
    }

    private fun updatePointerState() {
        val shouldShow = sessionId != null && GesturePointerSettings.enabled(this) && Settings.canDrawOverlays(this)
        if (!shouldShow) {
            pointerOverlay?.close()
            pointerOverlay = null
            gestureStream?.close()
            gestureStream = null
            return
        }
        if (pointerOverlay == null) pointerOverlay = GesturePointerOverlay(this)
        if (gestureStream == null) {
            val pairing = activePairing ?: return
            val id = sessionId ?: return
            gestureStream = GestureEventStream(
                pairing = pairing,
                sessionId = id,
                onGesture = { pointerOverlay?.show(it) },
                onFatal = { /* Screen sharing remains available if the visual pointer channel is unavailable. */ },
            ).also { it.start() }
        }
    }

    private fun configure(width: Int, height: Int) {
        if (width < 1 || height < 1 || closing.get()) return
        sourceWidth = width
        sourceHeight = height
        val scale = minOf(1f, 960f / maxOf(width, height))
        val outputWidth = (2 * (width * scale / 2).roundToInt()).coerceAtLeast(2)
        val outputHeight = (2 * (height * scale / 2).roundToInt()).coerceAtLeast(2)
        val nextEncoder = if (allowVideo) runCatching {
            AvcEncoder(outputWidth, outputHeight, bitRate, captureHandler, repeatLastFrame = true,
                onFrame = ::encodedFrame, onError = { captureHandler.post { if (encoder != null) useJpegCapture() } })
        }.getOrElse {
            allowVideo = false
            mediaStream?.close()
            mediaStream = null
            null
        } else null
        val nextReader = if (nextEncoder == null) {
            ImageReader.newInstance(outputWidth, outputHeight, PixelFormat.RGBA_8888, 2).apply {
                setOnImageAvailableListener(::frameAvailable, captureHandler)
            }
        } else null
        try {
            attachCaptureSurface(outputWidth, outputHeight, nextEncoder?.surface ?: checkNotNull(nextReader).surface)
        } catch (error: RuntimeException) {
            nextEncoder?.close()
            nextReader?.close()
            throw error
        }
        encoder?.close()
        reader?.close()
        encoder = nextEncoder
        reader = nextReader
        lastFrame = 0L
    }

    private fun attachCaptureSurface(outputWidth: Int, outputHeight: Int, surface: Surface) {
        val source = projection ?: throw IllegalStateException("The screen projection is unavailable.")
        val density = resources.configuration.densityDpi
        val existing = display
        if (existing == null) {
            display = source.createVirtualDisplay("mobile-use screen sharing", outputWidth, outputHeight, density,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR, surface, null, captureHandler)
                ?: throw IllegalStateException("Android did not create a virtual display.")
        } else {
            existing.resize(outputWidth, outputHeight, density)
            existing.surface = surface
        }
    }

    private fun frameDelivered() {
        if (closing.get()) return
        val uploadedAt = SystemClock.elapsedRealtime()
        lastSuccess = uploadedAt
        val firstOrRecovering = lastDeliveredAt == null || Sharing.error.value != null
        lastDeliveredAt = uploadedAt
        Sharing.hasFrames.value = true
        if (firstOrRecovering) reportError(null)
    }

    private fun encodedFrame(output: AvcEncoder, pts: Long, bytes: ByteArray, key: Boolean) {
        if (closing.get() || encoder !== output) return
        val now = SystemClock.elapsedRealtime()
        lastCapturedAt = now
        val stream = mediaStream ?: return
        stream.send(pts, bytes, key)
        val interval = if (stream.previewRequested) frameIntervalMs else SNAPSHOT_INTERVAL_MS
        if (now - lastFrame < interval || !canUploadJpeg() || !uploading.compareAndSet(false, true)) return
        lastFrame = now
        val bitmap = Bitmap.createBitmap(output.width, output.height, Bitmap.Config.ARGB_8888)
        runCatching {
            PixelCopy.request(output.surface, bitmap, { result ->
                if (result == PixelCopy.SUCCESS && encoder === output && !closing.get()) {
                    uploadBitmap(bitmap)
                } else {
                    bitmap.recycle()
                    uploading.set(false)
                }
            }, captureHandler)
        }.onFailure {
            bitmap.recycle()
            uploading.set(false)
        }
    }

    private fun frameAvailable(source: ImageReader) {
        if (closing.get() || reader !== source) return
        val image = runCatching { source.acquireLatestImage() }.getOrNull() ?: return
        image.use { frame ->
            val now = SystemClock.elapsedRealtime()
            lastCapturedAt = now
            if (now - lastFrame < frameIntervalMs || !canUploadJpeg() || !uploading.compareAndSet(false, true)) return
            lastFrame = now
            val bitmap = runCatching {
                val plane = frame.planes[0]
                val padded = Bitmap.createBitmap(plane.rowStride / plane.pixelStride, frame.height, Bitmap.Config.ARGB_8888)
                var crop: Bitmap? = null
                try {
                    padded.copyPixelsFromBuffer(plane.buffer)
                    Bitmap.createBitmap(padded, 0, 0, frame.width, frame.height).also { crop = it }
                } finally { if (padded !== crop) padded.recycle() }
            }.getOrNull()
            if (bitmap == null) {
                reportError("The phone could not encode a screen frame. Restart screen sharing and try again.")
                uploading.set(false)
                return
            }
            uploadBitmap(bitmap)
        }
    }

    private fun canUploadJpeg(): Boolean = frameStream?.mode() in setOf(FrameStreamMode.READY, FrameStreamMode.LEGACY)

    private fun uploadBitmap(bitmap: Bitmap) {
        val stream = frameStream
        val client = api
        val id = sessionId
        transfer.submit {
            try {
                if (closing.get() || stream == null || client == null || id == null) return@submit
                val bytes = ByteArrayOutputStream().also { out ->
                    check(bitmap.compress(Bitmap.CompressFormat.JPEG, 68, out))
                }.toByteArray()
                when (stream.mode()) {
                    FrameStreamMode.READY -> stream.send(bytes)
                    FrameStreamMode.LEGACY -> {
                        client.frame(id, bytes)
                        captureHandler.post(::frameDelivered)
                    }
                    else -> Unit
                }
            } catch (error: ApiFailure) {
                if (error.status != 429) reportError(error.message)
                if (error.status in setOf(401, 403, 404, 409)) stopSelf()
            } catch (error: Exception) {
                reportError(error.message ?: "The phone could not send a screen frame. Check Wi-Fi and the server address.")
            } finally {
                bitmap.recycle()
                uploading.set(false)
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
        gestureStream?.close()
        gestureStream = null
        pointerOverlay?.close()
        pointerOverlay = null
        frameStream?.close()
        val id = sessionId
        val client = api
        captureHandler.post {
            mediaStream?.close()
            if (id != null && client != null) transfer.submit { runCatching { client.session(id, "end") } }
            transfer.shutdown()
            control.shutdown()
            display?.release()
            encoder?.close()
            reader?.close()
            projection?.stop()
            captureThread.quitSafely()
        }
        Sharing.session.value = null
        Sharing.hasFrames.value = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val CHANNEL = "screen-sharing"
        private const val NOTIFICATION_ID = 100
        private const val LAN_FRAME_INTERVAL_MS = 125L
        private const val REMOTE_FRAME_INTERVAL_MS = 200L
        private const val SNAPSHOT_INTERVAL_MS = 750L
        private const val FRAME_STALE_MS = 3500L
        private const val FIRST_FRAME_RECOVERY_MS = 6000L
        private const val FIRST_FRAME_TIMEOUT_MS = 15000L
        private const val FRAME_ACK_TIMEOUT_MS = 5000L
        private const val STOP = "org.mobileuse.companion.STOP"
        private const val UPDATE_POINTER_OVERLAY = "org.mobileuse.companion.UPDATE_POINTER_OVERLAY"
        private const val SESSION = "session"
        private const val PERMISSION = "permission"
        private const val RESULT = "result"

        fun start(context: Context, sessionId: String, result: Int, permission: Intent) {
            context.startForegroundService(Intent(context, ScreenShareService::class.java)
                .putExtra(SESSION, sessionId).putExtra(RESULT, result).putExtra(PERMISSION, permission))
        }

        fun stop(context: Context) { context.stopService(Intent(context, ScreenShareService::class.java)) }

        fun updatePointerOverlay(context: Context) {
            runCatching {
                context.startService(Intent(context, ScreenShareService::class.java).setAction(UPDATE_POINTER_OVERLAY))
            }
        }
    }
}
