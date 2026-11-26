package org.mobileuse.companion

import android.Manifest
import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.hardware.camera2.params.OutputConfiguration
import android.hardware.camera2.params.SessionConfiguration
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaCodec
import android.media.MediaFormat
import android.media.MediaRecorder
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.SystemClock
import android.util.Log
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.flow.MutableStateFlow
import kotlin.math.abs

internal object SensorSharing {
    val camera = MutableStateFlow<String?>(null)
    val microphone = MutableStateFlow<String?>(null)
    val error = MutableStateFlow<String?>(null)
}

abstract class SensorShareService : Service() {
    internal abstract val source: MediaSource
    protected val thread = HandlerThread("mobile-use-sensor")
    protected lateinit var capture: Handler
    protected val closing = AtomicBoolean()
    private val control = Executors.newSingleThreadExecutor()
    private val checking = AtomicBoolean()
    private var stream: LiveMediaSocket? = null
    private var lastFeedback = 0L
    private var startedCapture = false
    private val state get() = if (source == MediaSource.CAMERA) SensorSharing.camera else SensorSharing.microphone
    private val notificationId get() = if (source == MediaSource.CAMERA) 101 else 102
    private val foregroundType get() = if (source == MediaSource.CAMERA) ServiceInfo.FOREGROUND_SERVICE_TYPE_CAMERA
        else ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE

    override fun onCreate() {
        super.onCreate()
        thread.start()
        capture = Handler(thread.looper)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (stream != null) return START_NOT_STICKY
        val id = intent?.getStringExtra(SESSION)
        val pairing = PairingStore(this).read()
        try {
            startForeground(notificationId, notification(), foregroundType)
        } catch (error: RuntimeException) {
            fail(error)
            return START_NOT_STICKY
        }
        if (id == null || pairing == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        state.value = id
        SensorSharing.error.value = null
        lastFeedback = SystemClock.elapsedRealtime()
        stream = LiveMediaSocket(pairing, id, source, capture,
            onReady = {
                if (!closing.get()) {
                    runCatching {
                        if (!startedCapture) {
                            startCapture()
                            startedCapture = true
                        } else connectionReady()
                    }.onFailure(::fail)
                }
            },
            onSample = { lastFeedback = SystemClock.elapsedRealtime() },
            onUnavailable = {
                SensorSharing.error.value = "This server needs an update to receive live ${source.id}."
                stopSelf()
            },
            onFatal = { SensorSharing.error.value = it; stopSelf() },
        ).also { it.start() }
        val api = CompanionApi(pairing)
        capture.post(object : Runnable {
            override fun run() {
                if (closing.get()) return
                if (SystemClock.elapsedRealtime() - lastFeedback > 12_000) {
                    SensorSharing.error.value = "${source.label} sharing stopped after losing the live connection."
                    stopSelf()
                    return
                }
                if (checking.compareAndSet(false, true)) control.submit {
                    try {
                        val status = api.heartbeat(id)
                        if (status.sessions.none { it.id == id && it.state == "active" && source.id in it.caps }) stopSelf()
                    } catch (error: ApiFailure) {
                        if (error.status in setOf(401, 403, 404)) stopSelf()
                    } catch (_: Exception) {
                        // The media watchdog stops capture if the connection stays down.
                    } finally { checking.set(false) }
                }
                capture.postDelayed(this, 2500)
            }
        })
        return START_NOT_STICKY
    }

    protected fun publish(pts: Long, data: ByteArray, key: Boolean = false) {
        if (!closing.get()) stream?.send(pts, data, key)
    }

    protected fun fail(error: Throwable) {
        if (closing.get()) return
        Log.w("MobileUseMedia", "${source.label} capture failed", error)
        SensorSharing.error.value = "${source.label} sharing could not continue. Check access and try again."
        stopSelf()
    }

    protected abstract fun startCapture()
    protected open fun connectionReady() = Unit
    protected open fun stopCapture() = Unit

    private fun notification(): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel(CHANNEL, "Camera and microphone sharing", NotificationManager.IMPORTANCE_LOW))
        val stop = PendingIntent.getService(this, notificationId, Intent(this, javaClass).setAction(STOP), PendingIntent.FLAG_IMMUTABLE)
        val open = PendingIntent.getActivity(this, notificationId, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setContentTitle("${source.label} sharing is on").setSmallIcon(R.drawable.ic_notification)
            .setVisibility(Notification.VISIBILITY_PRIVATE).setOngoing(true).setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Stop", stop).build()).build()
    }

    override fun onDestroy() {
        closing.set(true)
        state.value = null
        stream?.close()
        capture.post {
            control.shutdown()
            stopCapture()
            thread.quitSafely()
        }
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val SESSION = "session"
        private const val STOP = "org.mobileuse.companion.STOP_SENSOR"
        private const val CHANNEL = "sensor-sharing"

        internal fun permission(source: MediaSource) = if (source == MediaSource.CAMERA) Manifest.permission.CAMERA
            else Manifest.permission.RECORD_AUDIO

        private fun service(source: MediaSource) = if (source == MediaSource.CAMERA) CameraShareService::class.java
            else MicrophoneShareService::class.java

        internal fun start(context: Context, source: MediaSource, id: String) {
            context.startForegroundService(Intent(context, service(source)).putExtra(SESSION, id))
        }

        internal fun stop(context: Context, source: MediaSource) {
            context.stopService(Intent(context, service(source)))
        }
    }
}

class CameraShareService : SensorShareService() {
    override val source get() = MediaSource.CAMERA
    private var encoder: AvcEncoder? = null
    private var camera: CameraDevice? = null
    private var session: CameraCaptureSession? = null

    @SuppressLint("MissingPermission") // MainActivity grants CAMERA before starting this foreground service.
    override fun startCapture() {
        val manager = getSystemService(CameraManager::class.java)
        val cameras = manager.cameraIdList.map { it to manager.getCameraCharacteristics(it) }
        val (id, characteristics) = cameras.firstOrNull { it.second.get(CameraCharacteristics.LENS_FACING) == CameraCharacteristics.LENS_FACING_BACK }
            ?: cameras.firstOrNull() ?: error("No camera is available")
        val sizes = characteristics.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)
            ?.getOutputSizes(MediaCodec::class.java).orEmpty()
        val size = sizes.minByOrNull { abs(it.width - 640) + abs(it.height - 480) } ?: error("No supported camera video size")
        val output = AvcEncoder(size.width, size.height, 900_000, capture,
            onFrame = { _, pts, data, key -> publish(pts, data, key) }, onError = ::fail)
        encoder = output
        val executor = Executor { task -> if (!capture.post(task)) mainExecutor.execute(task) }
        manager.openCamera(id, executor, object : CameraDevice.StateCallback() {
            override fun onOpened(device: CameraDevice) {
                if (closing.get()) { device.close(); return }
                camera = device
                runCatching {
                    val callback = object : CameraCaptureSession.StateCallback() {
                        override fun onConfigured(current: CameraCaptureSession) {
                            if (closing.get()) { current.close(); return }
                            session = current
                            runCatching {
                                val request = device.createCaptureRequest(CameraDevice.TEMPLATE_RECORD).apply {
                                    addTarget(output.surface)
                                    characteristics.get(CameraCharacteristics.CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES)
                                        ?.minByOrNull { abs(it.upper - VIDEO_FPS) + abs(it.lower - VIDEO_FPS) }
                                        ?.let { set(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, it) }
                                }.build()
                                current.setRepeatingRequest(request, null, capture)
                            }.onFailure(::fail)
                        }

                        override fun onConfigureFailed(current: CameraCaptureSession) {
                            current.close()
                            fail(IllegalStateException("Camera capture session could not start"))
                        }
                    }
                    device.createCaptureSession(SessionConfiguration(SessionConfiguration.SESSION_REGULAR,
                        listOf(OutputConfiguration(output.surface)), executor, callback))
                }.onFailure(::fail)
            }

            override fun onDisconnected(device: CameraDevice) {
                device.close()
                fail(IllegalStateException("Camera disconnected"))
            }

            override fun onError(device: CameraDevice, error: Int) {
                device.close()
                fail(IllegalStateException("Camera error $error"))
            }
        })
    }

    override fun connectionReady() { encoder?.requestKeyFrame() }

    override fun stopCapture() {
        session?.close()
        camera?.close()
        encoder?.close()
    }
}

class MicrophoneShareService : SensorShareService() {
    override val source get() = MediaSource.MICROPHONE
    @Volatile private var activeRecorder: AudioRecord? = null

    override fun startCapture() {
        Thread({ runCatching { recordLive() }.onFailure(::fail) }, "mobile-use-audio").start()
    }

    override fun stopCapture() {
        runCatching { activeRecorder?.stop() }
    }

    @SuppressLint("MissingPermission") // MainActivity grants RECORD_AUDIO before starting this foreground service.
    private fun recordLive() {
        val sampleRate = 48_000
        val channel = AudioFormat.CHANNEL_IN_MONO
        val pcm = AudioFormat.ENCODING_PCM_16BIT
        val chunk = 960 * 2 // 20 ms of mono PCM16.
        val minimum = AudioRecord.getMinBufferSize(sampleRate, channel, pcm)
        check(minimum > 0) { "48 kHz microphone capture is unavailable" }
        val recorder = AudioRecord(MediaRecorder.AudioSource.MIC, sampleRate, channel, pcm, maxOf(minimum, chunk * 2))
        var encoder: MediaCodec? = null
        try {
            activeRecorder = recorder
            if (closing.get()) return
            check(recorder.state == AudioRecord.STATE_INITIALIZED) { "The microphone is unavailable" }
            val opus = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_AUDIO_OPUS).also { encoder = it }
            val format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_OPUS, sampleRate, 1).apply {
                setInteger(MediaFormat.KEY_BIT_RATE, 32_000)
            }
            opus.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            opus.start()
            recorder.startRecording()
            var samples = 0L
            val info = MediaCodec.BufferInfo()
            while (!closing.get()) {
                val input = opus.dequeueInputBuffer(10_000)
                if (input >= 0) {
                    val buffer = checkNotNull(opus.getInputBuffer(input))
                    val count = recorder.read(buffer, chunk, AudioRecord.READ_BLOCKING)
                    check(count > 0) { "The microphone stopped recording" }
                    opus.queueInputBuffer(input, 0, count, samples * 1_000_000 / sampleRate, 0)
                    samples += count / 2
                }
                while (!closing.get()) {
                    val index = opus.dequeueOutputBuffer(info, 0)
                    if (index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) continue
                    if (index < 0) break
                    try {
                        if (info.size > 0 && info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG == 0) {
                            val data = ByteArray(info.size)
                            val buffer = checkNotNull(opus.getOutputBuffer(index))
                            buffer.position(info.offset)
                            buffer.get(data)
                            publish(info.presentationTimeUs, data)
                        }
                    } finally { opus.releaseOutputBuffer(index, false) }
                }
            }
        } finally {
            activeRecorder = null
            runCatching { if (recorder.recordingState == AudioRecord.RECORDSTATE_RECORDING) recorder.stop() }
            recorder.release()
            encoder?.release()
        }
    }
}
