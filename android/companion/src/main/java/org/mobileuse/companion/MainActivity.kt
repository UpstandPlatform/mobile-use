package org.mobileuse.companion

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import androidx.core.view.WindowCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import java.io.File
import java.net.URI
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    private companion object {
        const val STATE_PROJECTION = "pending.projection"
        const val STATE_PHOTO_FILE = "pending.photo.file"
        const val STATE_PHOTO_REQUEST = "pending.photo.request"
        const val STATE_MIC_REQUEST = "pending.mic.request"
        const val STATE_LIVE_SENSOR = "pending.live.sensor"
        const val STATE_LIVE_SESSION = "pending.live.session"
    }

    private lateinit var store: PairingStore
    private lateinit var lanDiscovery: LanDiscovery
    private var pairing by mutableStateOf<Pairing?>(null)
    private var status by mutableStateOf(Heartbeat(emptyList(), emptyList()))
    private var message by mutableStateOf("")
    private var attending by mutableStateOf<String?>(null)
    private var busy by mutableStateOf(false)
    private var online by mutableStateOf(false)
    private var backgroundMonitorEnabled by mutableStateOf(false)
    private var gesturePointerEnabled by mutableStateOf(false)
    private var accessibilityControlEnabled by mutableStateOf(false)
    private var controlSessionId: String? = null
    private var notificationsAvailable by mutableStateOf(false)
    private var backgroundNotificationsPending = false
    private var gesturePointerPermissionPending = false
    private var scanningQr by mutableStateOf(false)
    private var pendingQr by mutableStateOf<PairingRequest?>(null)
    private var projectionId: String? = null
    private var micRequest: SensorRequest? = null
    private var livePermission: Pair<MediaSource, String>? = null
    private var micJob: Job? = null
    private var photoRequest: SensorRequest? = null
    private var photoFile: File? = null

    private val projection = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        val id = projectionId ?: return@registerForActivityResult
        projectionId = null
        val approval = result.data
        if (result.resultCode == Activity.RESULT_OK && approval != null) {
            runCatching { ScreenShareService.start(this, id, result.resultCode, approval) }
                .onFailure { message = "Screen sharing could not start." }
        } else { message = "Screen sharing was declined." }
    }
    private val notifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) { requestProjection() }
    private val backgroundNotificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        notificationsAvailable = CompanionNotifications.notificationsAllowed(this)
        if (backgroundNotificationsPending) {
            backgroundNotificationsPending = false
            if (granted && pairing != null && notificationsAvailable) {
                setBackgroundConnectionEnabled(true)
            } else {
                backgroundMonitorEnabled = false
                message = "Allow mobile-use notifications in Android Settings to keep approvals available in the background."
            }
        }
    }
    private val cameraScan = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) {
            message = ""
            scanningQr = true
        } else {
            message = "Camera permission is required to scan a pairing QR code."
        }
    }
    private val microphone = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val request = micRequest ?: return@registerForActivityResult
        micRequest = null
        if (!granted || !record(request)) resolveSensor { it.denySensor(request.id) }
    }
    private val liveSensorPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val (source, id) = livePermission ?: return@registerForActivityResult
        livePermission = null
        if (granted) startSensorShare(source, id)
        else message = "${source.label} permission was denied."
    }
    private val cameraCapturePermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val request = photoRequest ?: return@registerForActivityResult
        photoRequest = null
        if (granted) launchCamera(request) else denySensorWithNotice(request.id, "Camera permission was denied.")
    }
    private val camera = registerForActivityResult(ActivityResultContracts.TakePicture()) { approved ->
        val request = photoRequest
        val file = photoFile
        photoRequest = null
        photoFile = null
        if (request == null) { file?.delete(); return@registerForActivityResult }
        if (file == null) { resolveSensor { it.denySensor(request.id) }; return@registerForActivityResult }
        resolveSensor(cleanup = { file.delete() }) {
            if (approved) it.upload(request.id, approvedPhoto(file), "image/jpeg") else it.denySensor(request.id)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        savedInstanceState?.let { restorePending(it) }
        WindowCompat.getInsetsController(window, window.decorView).apply {
            isAppearanceLightStatusBars = true
            isAppearanceLightNavigationBars = true
        }
        store = PairingStore(this)
        lanDiscovery = LanDiscovery(this)
        pairing = store.read()
        gesturePointerEnabled = GesturePointerSettings.enabled(this)
        accessibilityControlEnabled = DeviceAccessibilityService.isConnected()
        CompanionNotifications.createChannels(this)
        notificationsAvailable = CompanionNotifications.notificationsAllowed(this)
        BackgroundConnectionSettings.refresh(this)
        backgroundMonitorEnabled = BackgroundConnectionSettings.enabled(this)
        if (backgroundMonitorEnabled && pairing != null && notificationsAvailable) {
            BackgroundMonitorService.startIfEnabled(this)
        } else if (backgroundMonitorEnabled) {
            BackgroundMonitorService.setEnabled(this, false)
            backgroundMonitorEnabled = false
        }
        handleDeepLink(intent)
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                while (true) {
                    val current = pairing
                    if (current == null) {
                        lanDiscovery.start()
                    } else {
                        lanDiscovery.stop()
                        try {
                            val value = withContext(Dispatchers.IO) { CompanionApi(current).heartbeat(attending, PhoneControlCapabilities.current(this@MainActivity)) }
                            if (pairing == current) {
                                status = value
                                online = true
                                syncControlSession(value)
                                if (attending != null && value.sessions.none { it.id == attending && it.state == "active" }) attending = null
                            }
                        } catch (cancelled: CancellationException) { throw cancelled }
                        catch (_: Exception) { online = false }
                    }
                    delay(2500)
                }
            }
        }
        setContent { CompanionUI() }
    }

    // The system may recreate this Activity while the camera, permission or screen-capture
    // screen is in front; keep what their results need to finish the pending request.
    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString(STATE_PROJECTION, projectionId)
        outState.putString(STATE_PHOTO_FILE, photoFile?.absolutePath)
        photoRequest?.let { outState.putStringArray(STATE_PHOTO_REQUEST, encodeSensor(it)) }
        micRequest?.let { outState.putStringArray(STATE_MIC_REQUEST, encodeSensor(it)) }
        livePermission?.let { (source, id) ->
            outState.putString(STATE_LIVE_SENSOR, source.id)
            outState.putString(STATE_LIVE_SESSION, id)
        }
    }

    private fun restorePending(state: Bundle) {
        projectionId = state.getString(STATE_PROJECTION)
        photoFile = state.getString(STATE_PHOTO_FILE)?.let { File(it) }
        photoRequest = state.getStringArray(STATE_PHOTO_REQUEST)?.let { decodeSensor(it) }
        micRequest = state.getStringArray(STATE_MIC_REQUEST)?.let { decodeSensor(it) }
        val sensor = MediaSource.entries.firstOrNull { it.id == state.getString(STATE_LIVE_SENSOR) }
        val liveSession = state.getString(STATE_LIVE_SESSION)
        if (sensor != null && liveSession != null) livePermission = sensor to liveSession
    }

    private fun encodeSensor(request: SensorRequest) = arrayOf(request.id, request.sessionId, request.type, request.seconds.toString())

    private fun decodeSensor(values: Array<String>): SensorRequest? =
        if (values.size == 4) values[3].toIntOrNull()?.let { SensorRequest(values[0], values[1], values[2], it) } else null

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
    }

    override fun onStop() {
        BackgroundConnectionRuntime.activityVisible = false
        lanDiscovery.stop()
        micJob?.cancel()
        super.onStop()
    }

    override fun onStart() {
        super.onStart()
        BackgroundConnectionRuntime.activityVisible = true
        CompanionNotifications.clearScreenShareError(this)
        if (BackgroundConnectionSettings.enabled(this)) CompanionNotifications.clearPending(this)
    }

    override fun onResume() {
        super.onResume()
        notificationsAvailable = CompanionNotifications.notificationsAllowed(this)
        BackgroundConnectionSettings.refresh(this)
        backgroundMonitorEnabled = BackgroundConnectionSettings.enabled(this)
        gesturePointerEnabled = GesturePointerSettings.enabled(this)
        accessibilityControlEnabled = DeviceAccessibilityService.isConnected()
        pairing?.let { syncControlSession(status) }
        if (gesturePointerPermissionPending) {
            gesturePointerPermissionPending = false
            if (Settings.canDrawOverlays(this)) {
                GesturePointerSettings.setEnabled(this, true)
                gesturePointerEnabled = true
                syncGesturePointerPreference(true)
                ScreenShareService.updatePointerOverlay(this)
                message = "Agent gesture pointer is on. It appears during active screen sharing."
            } else {
                GesturePointerSettings.setEnabled(this, false)
                gesturePointerEnabled = false
                message = "Allow mobile-use to display over other apps to enable the gesture pointer."
            }
        } else if (gesturePointerEnabled && !Settings.canDrawOverlays(this)) {
            GesturePointerSettings.setEnabled(this, false)
            gesturePointerEnabled = false
            syncGesturePointerPreference(false)
            ScreenShareService.updatePointerOverlay(this)
            message = "The gesture pointer was turned off because Android overlay access was revoked."
        }
        if (backgroundMonitorEnabled && pairing != null && notificationsAvailable) {
            BackgroundMonitorService.startIfEnabled(this)
        } else if (backgroundMonitorEnabled && !notificationsAvailable) {
            BackgroundMonitorService.setEnabled(this, false)
            backgroundMonitorEnabled = false
            message = "Background connection paused because Android notifications are disabled."
        }
    }

    private fun setBackgroundConnectionEnabled(enabled: Boolean) {
        if (!enabled) {
            BackgroundMonitorService.setEnabled(this, false)
            backgroundMonitorEnabled = false
            return
        }
        if (pairing == null) {
            message = "Pair this phone before enabling background connection."
            return
        }
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            backgroundNotificationsPending = true
            backgroundNotificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
            return
        }
        notificationsAvailable = CompanionNotifications.notificationsAllowed(this)
        backgroundMonitorEnabled = BackgroundMonitorService.setEnabled(this, true)
        if (!backgroundMonitorEnabled) {
            message = "Background connection could not start. Allow notifications and check Android battery settings."
        } else {
            message = "Background connection is on. Android will show an ongoing notification."
        }
    }

    private fun syncControlSession(heartbeat: Heartbeat) {
        accessibilityControlEnabled = DeviceAccessibilityService.isConnected()
        val active = heartbeat.sessions.firstOrNull { session ->
            session.state == "active" && !session.paused && session.caps.any { it in setOf("ui_tree", "touch", "text", "apps", "browser") }
        }
        if (active?.id == controlSessionId) return
        ControlSessionService.stop(this)
        controlSessionId = active?.id
        if (active != null && pairing != null) {
            runCatching { ControlSessionService.start(this, active.id) }
                .onFailure { message = "The approved control service could not start. Allow notifications and try again." }
        }
    }

    private fun openAccessibilitySettings() {
        runCatching { startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)) }
            .onFailure { message = "Open Android Settings → Accessibility to enable mobile-use device control." }
    }

    private fun openBatterySettings() {
        runCatching {
            startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).setData(Uri.parse("package:$packageName")))
        }.onFailure { message = "Open Android Settings and find mobile-use under Apps to change its battery use." }
    }

    private fun openNotificationSettings() {
        runCatching {
            startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, packageName))
        }.onFailure { message = "Open Android Settings and allow notifications for mobile-use." }
    }

    private fun handleGesturePointerToggle(enabled: Boolean) {
        if (!enabled) {
            gesturePointerPermissionPending = false
            GesturePointerSettings.setEnabled(this, false)
            gesturePointerEnabled = false
            ScreenShareService.updatePointerOverlay(this)
            return
        }
        if (!Settings.canDrawOverlays(this)) {
            gesturePointerPermissionPending = true
            runCatching {
                startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:$packageName")))
            }.onFailure {
                gesturePointerPermissionPending = false
                message = "Open Android Settings and allow mobile-use to display over other apps."
            }
            return
        }
        GesturePointerSettings.setEnabled(this, true)
        gesturePointerEnabled = true
        syncGesturePointerPreference(true)
        ScreenShareService.updatePointerOverlay(this)
        message = "Agent gesture pointer is on. It appears during active screen sharing."
    }

    private fun syncGesturePointerPreference(enabled: Boolean) {
        val current = pairing ?: return
        lifecycleScope.launch {
            runCatching {
                withContext(Dispatchers.IO) { CompanionApi(current).gesturePointer(enabled) }
            }
        }
    }

    private fun handleDeepLink(intent: Intent?) {
        val data = intent?.dataString ?: return
        if (!data.startsWith("mobile-use://pair")) return
        runCatching { parsePairingUri(data) }
            .onSuccess {
                if (pairing == null) {
                    pendingQr = it
                    message = ""
                }
            }
            .onFailure { err ->
                message = err.message ?: "The pairing link is invalid."
            }
    }

    private fun requestProjection() {
        if (projectionId != null) projection.launch(getSystemService(MediaProjectionManager::class.java).createScreenCaptureIntent())
    }

    private fun share(id: String) {
        projectionId = id
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            notifications.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else requestProjection()
    }

    private fun execute(task: suspend (CompanionApi) -> Unit) {
        val current = pairing ?: return
        if (busy) return
        busy = true
        message = ""
        lifecycleScope.launch {
            try { withContext(Dispatchers.IO) { task(CompanionApi(current)) } }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { message = if (error is ApiFailure) error.message.orEmpty() else "The request could not be completed." }
            finally { busy = false }
        }
    }

    /**
     * Resolves a sensor request from an activity result. Unlike [execute] it never drops the
     * work because another request is busy, so the server is not left waiting, and [cleanup]
     * always runs (also when the phone was unpaired meanwhile).
     */
    private fun resolveSensor(cleanup: () -> Unit = {}, task: suspend (CompanionApi) -> Unit) {
        val current = pairing
        if (current == null) { cleanup(); return }
        lifecycleScope.launch {
            try { withContext(Dispatchers.IO) { task(CompanionApi(current)) } }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { message = if (error is ApiFailure) error.message.orEmpty() else "The request could not be completed." }
            finally { cleanup() }
        }
    }

    /** Returns false when the clip could not start, so the caller can resolve the request. */
    private fun record(request: SensorRequest): Boolean {
        val current = pairing ?: return false
        if (busy) return false
        busy = true
        message = "Recording microphone clip…"
        micJob = lifecycleScope.launch {
            try {
                withContext(Dispatchers.IO) { CompanionApi(current).upload(request.id, microphoneClip(this@MainActivity, request.seconds), "audio/wav") }
                message = "Microphone clip sent."
            } catch (cancelled: CancellationException) { message = "Microphone recording stopped."; throw cancelled }
            catch (_: Exception) { message = "The microphone clip could not be sent." }
            finally { busy = false }
        }
        return true
    }

    private fun takePhoto(request: SensorRequest) {
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            photoRequest = request
            cameraCapturePermission.launch(Manifest.permission.CAMERA)
            return
        }
        launchCamera(request)
    }

    private fun launchCamera(request: SensorRequest) {
        val folder = File(cacheDir, "approved-photos").apply { mkdirs() }
        val file = File.createTempFile("photo-", ".jpg", folder)
        photoRequest = request
        photoFile = file
        try { camera.launch(FileProvider.getUriForFile(this, "$packageName.photos", file)) }
        catch (_: Exception) {
            file.delete(); photoFile = null; photoRequest = null
            denySensorWithNotice(request.id, "A system camera app is unavailable.")
        }
    }

    private fun denySensorWithNotice(requestId: String, notice: String) {
        resolveSensor {
            it.denySensor(requestId)
            withContext(Dispatchers.Main) { message = notice }
        }
    }

    private fun shareSensor(source: MediaSource, id: String) {
        if (livePermission != null) return
        val permission = SensorShareService.permission(source)
        if (checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED) {
            startSensorShare(source, id)
        } else {
            livePermission = source to id
            liveSensorPermission.launch(permission)
        }
    }

    private fun startSensorShare(source: MediaSource, id: String) {
        if (pairing == null) return
        message = ""
        runCatching { SensorShareService.start(this, source, id) }
            .onFailure { message = "${source.label} sharing could not start." }
    }

    private fun stopSession(id: String) {
        val current = pairing ?: return
        attending = null
        projectionId = null
        micRequest = null
        livePermission = null
        micJob?.cancel()
        ScreenShareService.stop(this)
        if (controlSessionId == id) {
            ControlSessionService.stop(this)
            controlSessionId = null
        }
        if (SensorSharing.camera.value == id) SensorShareService.stop(this, MediaSource.CAMERA)
        if (SensorSharing.microphone.value == id) SensorShareService.stop(this, MediaSource.MICROPHONE)
        Sharing.error.value = null
        Sharing.hasFrames.value = false
        CompanionNotifications.clearScreenShareError(this)
        lifecycleScope.launch {
            try {
                withContext(Dispatchers.IO) { CompanionApi(current).session(id, "end") }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { message = "Sharing stopped on this phone. The console will update when it reconnects." }
        }
    }

    private fun performEnroll(
        origin: String,
        code: String,
        certPin: String,
        hostName: String,
        mode: String,
        onSuccess: () -> Unit,
    ) {
        if (busy) return
        busy = true
        message = ""
        lifecycleScope.launch {
            try {
                pairing = withContext(Dispatchers.IO) {
                    CompanionApi.enroll(origin, code, Build.MODEL, certPin, hostName, mode).also(store::save)
                }
                pendingQr = null
                onSuccess()
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: IllegalArgumentException) { message = error.message ?: "Check the pairing details." }
            catch (error: ApiFailure) { message = error.message.orEmpty() }
            catch (_: Exception) { message = "Check the server address, Wi-Fi connection, and pairing code." }
            finally { busy = false }
        }
    }

    private fun launchQrScanner() {
        if (checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
            message = ""
            scanningQr = true
        } else {
            cameraScan.launch(Manifest.permission.CAMERA)
        }
    }

    @Composable
    private fun CompanionUI() {
        val sharing by Sharing.session.collectAsState()
        val sharingError by Sharing.error.collectAsState()
        val hasFrames by Sharing.hasFrames.collectAsState()
        val cameraShare by SensorSharing.camera.collectAsState()
        val microphoneShare by SensorSharing.microphone.collectAsState()
        val sensorError by SensorSharing.error.collectAsState()
        val backgroundConnectionOn by BackgroundConnectionSettings.state.collectAsState()
        val discoveredHosts by lanDiscovery.hosts.collectAsState()
        var origin by rememberSaveable { mutableStateOf("") }
        var code by remember { mutableStateOf("") }
        var manualMode by rememberSaveable { mutableStateOf("lan") }
        var certPin by rememberSaveable { mutableStateOf("") }
        var selectedHost by remember { mutableStateOf<DiscoveredHost?>(null) }
        var showManual by rememberSaveable { mutableStateOf(false) }
        var showAdvanced by rememberSaveable { mutableStateOf(false) }
        var confirmDisconnect by remember { mutableStateOf(false) }

        val colors = if (isSystemInDarkTheme()) darkColorScheme(
            primary = Color(0xFFC1E49F),
            onPrimary = Color(0xFF26391B),
            primaryContainer = Color(0xFF354B28),
            onPrimaryContainer = Color(0xFFE0F4C9),
            secondaryContainer = Color(0xFF394633),
            onSecondaryContainer = Color(0xFFD9E7CA),
            background = Color(0xFF1D211A),
            surface = Color(0xFF1D211A),
            surfaceVariant = Color(0xFF43483B),
            onSurfaceVariant = Color(0xFFC4CABB),
        ) else lightColorScheme(
            primary = Color(0xFF405B30),
            onPrimary = Color(0xFFFFFFFF),
            primaryContainer = Color(0xFFD7ED99),
            onPrimaryContainer = Color(0xFF243419),
            secondaryContainer = Color(0xFFE3EBD9),
            onSecondaryContainer = Color(0xFF2D3C24),
            background = Color(0xFFF5F5EF),
            surface = Color(0xFFFDFDF8),
        )
        MaterialTheme(colorScheme = colors) {
            if (pairing == null && scanningQr) {
                QrScannerScreen(
                    onScanned = { req ->
                        pendingQr = req
                        scanningQr = false
                        message = ""
                    },
                    onCancel = { scanningQr = false },
                )
                return@MaterialTheme
            }

            Surface(modifier = Modifier.fillMaxSize(), color = colors.background) {
              Box(Modifier.fillMaxSize().safeDrawingPadding().imePadding(), contentAlignment = Alignment.TopCenter) {
                Column(Modifier.widthIn(max = 520.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp, vertical = 24.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalAlignment = Alignment.CenterVertically) {
                        Image(painterResource(R.drawable.mobile_use_icon), null, Modifier.size(36.dp))
                        Column(verticalArrangement = Arrangement.spacedBy(1.dp)) {
                            Text("mobile-use", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                            Text("Phone companion", style = MaterialTheme.typography.labelSmall, color = colors.onSurfaceVariant)
                        }
                    }

                    if (pairing == null) {
                        ElevatedCard(
                            modifier = Modifier.fillMaxWidth(),
                            colors = CardDefaults.elevatedCardColors(containerColor = colors.primaryContainer),
                        ) {
                            Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                                Text("Connect to your workspace", style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.SemiBold)
                                Text(
                                    "Pair once to let your computer request screen sharing and approved sensors. Your phone stays in control of each permission.",
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = colors.onSurfaceVariant,
                                )
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    listOf("QR code", "Same Wi-Fi", "Remote HTTPS").forEach { label ->
                                        Surface(color = colors.surface.copy(alpha = 0.72f), shape = MaterialTheme.shapes.small) {
                                            Text(label, Modifier.padding(horizontal = 10.dp, vertical = 6.dp), style = MaterialTheme.typography.labelMedium, color = colors.onSurface)
                                        }
                                    }
                                }
                            }
                        }

                        val qrReq = pendingQr
                        if (qrReq != null) {
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                                    Text("Confirm pairing", style = MaterialTheme.typography.titleMedium)
                                    Text(
                                        qrReq.hostName.ifEmpty { runCatching { URI(qrReq.origin).host }.getOrDefault(qrReq.origin) },
                                        style = MaterialTheme.typography.bodyLarge,
                                        fontWeight = FontWeight.SemiBold,
                                    )
                                    Text(qrReq.origin, color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
                                    Text(
                                        if (qrReq.mode == "lan") "Local Wi-Fi (Pinned TLS)" else "Remote HTTPS",
                                        color = colors.primary,
                                        style = MaterialTheme.typography.labelMedium,
                                    )
                                    if (qrReq.certPin.isNotEmpty()) {
                                        Text(
                                            "Fingerprint: ${formatShortFingerprint(qrReq.certPin)}",
                                            fontFamily = FontFamily.Monospace,
                                            style = MaterialTheme.typography.bodySmall,
                                            color = colors.onSurfaceVariant,
                                        )
                                    }
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(
                                            enabled = !busy,
                                            modifier = Modifier.weight(1f),
                                            onClick = {
                                                performEnroll(qrReq.origin, qrReq.code, qrReq.certPin, qrReq.hostName, qrReq.mode) {
                                                    code = ""
                                                }
                                            },
                                        ) { Text(if (busy) "Pairing…" else "Pair now") }
                                        OutlinedButton(enabled = !busy, onClick = { pendingQr = null }) { Text("Cancel") }
                                    }
                                }
                            }
                        }

                        Button(
                            enabled = !busy,
                            modifier = Modifier.fillMaxWidth(),
                            onClick = ::launchQrScanner,
                        ) { Text(if (busy) "Connecting…" else "Scan pairing QR code") }

                        val picked = selectedHost
                        if (picked != null) {
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                                    Text("Pair with ${picked.hostName}", style = MaterialTheme.typography.titleMedium)
                                    Text(picked.origin, color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
                                    if (picked.shortFingerprint.isNotEmpty()) {
                                        Text(
                                            "Verify fingerprint: ${picked.shortFingerprint}",
                                            fontFamily = FontFamily.Monospace,
                                            style = MaterialTheme.typography.bodySmall,
                                            color = colors.primary,
                                        )
                                    }
                                    OutlinedTextField(
                                        value = code,
                                        onValueChange = { code = it.trim().uppercase() },
                                        label = { Text("8-character pairing code") },
                                        placeholder = { Text("XXXX-XXXX") },
                                        singleLine = true,
                                        enabled = !busy,
                                        keyboardOptions = KeyboardOptions(
                                            capitalization = KeyboardCapitalization.Characters,
                                            keyboardType = KeyboardType.Ascii,
                                            autoCorrectEnabled = false,
                                            imeAction = ImeAction.Done,
                                        ),
                                        modifier = Modifier.fillMaxWidth(),
                                    )
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(
                                            enabled = !busy && code.isNotBlank(),
                                            modifier = Modifier.weight(1f),
                                            onClick = {
                                                performEnroll(picked.origin, code, picked.certPin, picked.hostName, "lan") {
                                                    code = ""
                                                    selectedHost = null
                                                }
                                            },
                                        ) { Text(if (busy) "Pairing…" else "Pair phone") }
                                        OutlinedButton(enabled = !busy, onClick = { selectedHost = null }) { Text("Back") }
                                    }
                                }
                            }
                        } else {
                            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                                Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                    Text("Nearby on Wi-Fi", style = MaterialTheme.typography.titleMedium)
                                    Text("No address or network setup needed on the same network.", style = MaterialTheme.typography.bodySmall, color = colors.onSurfaceVariant)
                                }
                                Surface(color = colors.secondaryContainer, shape = MaterialTheme.shapes.small) {
                                    Text("Local", Modifier.padding(horizontal = 10.dp, vertical = 6.dp), style = MaterialTheme.typography.labelMedium, color = colors.onSecondaryContainer)
                                }
                            }
                            if (discoveredHosts.isEmpty()) {
                                OutlinedCard(Modifier.fillMaxWidth()) {
                                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                                        Text("Searching for your computer…", style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium)
                                        Text("Keep mobile-use open on the computer and connect both devices to the same Wi-Fi. For remote servers, use the address option below.", color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
                                    }
                                }
                            } else {
                                discoveredHosts.forEach { host ->
                                    OutlinedCard(
                                        onClick = { selectedHost = host; message = "" },
                                        modifier = Modifier.fillMaxWidth(),
                                    ) {
                                        Row(
                                            Modifier.fillMaxWidth().padding(16.dp),
                                            horizontalArrangement = Arrangement.SpaceBetween,
                                            verticalAlignment = Alignment.CenterVertically,
                                        ) {
                                            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                                Text(host.hostName, style = MaterialTheme.typography.titleSmall)
                                                Text(host.origin, color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodySmall)
                                                if (host.shortFingerprint.isNotEmpty()) {
                                                    Text(
                                                        "FP ${host.shortFingerprint}",
                                                        fontFamily = FontFamily.Monospace,
                                                        style = MaterialTheme.typography.labelSmall,
                                                        color = colors.onSurfaceVariant,
                                                    )
                                                }
                                            }
                                            Text("Select", color = colors.primary, style = MaterialTheme.typography.labelLarge)
                                        }
                                    }
                                }
                            }
                        }

                        TextButton(onClick = { showManual = !showManual }) {
                            Text(if (showManual) "Hide server address setup" else "Pair with a server address or remote workspace")
                        }

                        if (showManual) {
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        FilterChip(
                                            selected = manualMode == "lan",
                                            onClick = { manualMode = "lan" },
                                            label = { Text("Local Wi-Fi") },
                                        )
                                        FilterChip(
                                            selected = manualMode == "remote",
                                            onClick = { manualMode = "remote"; certPin = "" },
                                            label = { Text("Remote HTTPS") },
                                        )
                                    }
                                    OutlinedTextField(
                                        origin,
                                        { origin = it },
                                        label = { Text("Server address") },
                                        placeholder = { Text(if (manualMode == "lan") "https://192.168.1.42:8444" else "https://mobile-use.example.com") },
                                        singleLine = true,
                                        enabled = !busy,
                                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next),
                                        modifier = Modifier.fillMaxWidth(),
                                    )
                                    OutlinedTextField(
                                        code,
                                        { code = it.trim().uppercase() },
                                        label = { Text("Pairing code") },
                                        placeholder = { Text("XXXX-XXXX") },
                                        singleLine = true,
                                        enabled = !busy,
                                        keyboardOptions = KeyboardOptions(
                                            capitalization = KeyboardCapitalization.Characters,
                                            keyboardType = KeyboardType.Ascii,
                                            autoCorrectEnabled = false,
                                            imeAction = ImeAction.Done,
                                        ),
                                        modifier = Modifier.fillMaxWidth(),
                                    )
                                    if (manualMode == "lan") {
                                        TextButton(onClick = { showAdvanced = !showAdvanced }) {
                                            Text(if (showAdvanced) "Hide certificate fingerprint" else "Add SHA-256 certificate fingerprint (optional)")
                                        }
                                        if (showAdvanced) {
                                            OutlinedTextField(
                                                certPin,
                                                { certPin = it },
                                                label = { Text("Certificate SHA-256 hex") },
                                                singleLine = true,
                                                enabled = !busy,
                                                modifier = Modifier.fillMaxWidth(),
                                            )
                                        }
                                    }
                                    Button(
                                        enabled = !busy && origin.isNotBlank() && code.isNotBlank(),
                                        modifier = Modifier.fillMaxWidth(),
                                        onClick = {
                                            performEnroll(origin, code, if (manualMode == "lan") certPin else "", "", manualMode) {
                                                code = ""
                                            }
                                        },
                                    ) { Text(if (busy) "Pairing…" else "Pair phone") }
                                }
                            }
                        }
                    } else {
                        val activePairing = pairing!!
                        val hostTitle = activePairing.hostName.ifEmpty {
                            runCatching { URI(activePairing.origin).host }.getOrDefault(activePairing.origin)
                        }
                        ElevatedCard(
                            modifier = Modifier.fillMaxWidth(),
                            colors = CardDefaults.elevatedCardColors(containerColor = if (online) colors.primaryContainer else colors.surfaceContainerLow),
                        ) {
                            Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                                    Text(if (online) "Phone connected" else "Waiting for computer", style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.SemiBold)
                                    Surface(color = if (online) colors.secondaryContainer else colors.surface, shape = MaterialTheme.shapes.small) {
                                        Text(if (online) "Online" else "Reconnecting", Modifier.padding(horizontal = 10.dp, vertical = 6.dp), style = MaterialTheme.typography.labelMedium, color = if (online) colors.onSecondaryContainer else colors.onSurfaceVariant)
                                    }
                                }
                                Text("$hostTitle · ${if (activePairing.mode == "lan") "Local Wi-Fi" else "Remote HTTPS"}", color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodyMedium)
                                Text(
                                    if (status.sessions.isEmpty()) "When you request a session in the console, review it here before sharing your screen or sensors."
                                    else "Review each request below. Screen and sensor permissions are approved on this phone.",
                                    color = colors.onSurfaceVariant,
                                    style = MaterialTheme.typography.bodySmall,
                                )
                            }
                        }
                        ElevatedCard(modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Row(
                                    Modifier.fillMaxWidth(),
                                    horizontalArrangement = Arrangement.SpaceBetween,
                                    verticalAlignment = Alignment.CenterVertically,
                                ) {
                                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                        Text("Background connection", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                        Text(
                                            if (backgroundConnectionOn) "On · approvals and alerts stay available while you leave the app."
                                            else "Off · this phone checks for requests while the app is open.",
                                            style = MaterialTheme.typography.bodySmall,
                                            color = colors.onSurfaceVariant,
                                        )
                                    }
                                    Switch(
                                        checked = backgroundConnectionOn,
                                        onCheckedChange = ::setBackgroundConnectionEnabled,
                                    )
                                }
                                Text(
                                    "When on, mobile-use uses network and may use battery, and Android keeps an ongoing notification. For better screen-off reliability, open app info and set battery use to Unrestricted where available. Android may still defer network access. Screen sharing needs a fresh capture approval, and a locked or powered-off screen may produce blank frames.",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = colors.onSurfaceVariant,
                                )
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    TextButton(onClick = ::openBatterySettings) { Text("Open app info") }
                                    if (!notificationsAvailable) {
                                        TextButton(onClick = ::openNotificationSettings) { Text("Notification settings") }
                                    }
                                }
                            }
                        }
                        ElevatedCard(modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Text("Agent device control", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                Text(
                                    if (accessibilityControlEnabled) "Accessibility control is enabled. Approved sessions can inspect the UI tree, tap, type, navigate, and interact with browser pages."
                                    else "Installed app discovery and launch are available. Enable Android accessibility to add UI-tree, touch, text, navigation, and browser control.",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = colors.onSurfaceVariant,
                                )
                                Text(
                                    "The service reads the visible interface only when an approved session asks it to. Android keeps control available while the session notification is active. Unlock the phone before screen actions.",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = colors.onSurfaceVariant,
                                )
                                OutlinedButton(onClick = ::openAccessibilitySettings) {
                                    Text(if (accessibilityControlEnabled) "Manage accessibility access" else "Enable accessibility control")
                                }
                            }
                        }
                        ElevatedCard(modifier = Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Row(
                                    Modifier.fillMaxWidth(),
                                    horizontalArrangement = Arrangement.SpaceBetween,
                                    verticalAlignment = Alignment.CenterVertically,
                                ) {
                                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                        Text("Agent gesture pointer", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                        Text(
                                            if (gesturePointerEnabled) "On · agent touch gestures appear as an animated cursor while sharing."
                                            else "Off · agent gestures stay hidden on this phone.",
                                            style = MaterialTheme.typography.bodySmall,
                                            color = colors.onSurfaceVariant,
                                        )
                                    }
                                    Switch(
                                        checked = gesturePointerEnabled,
                                        onCheckedChange = ::handleGesturePointerToggle,
                                    )
                                }
                                Text(
                                    "The pointer is visual only: taps pass through to the app. Android asks for display-over-other-apps access once. Secure overlay rendering keeps the pointer out of shared screen frames.",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = colors.onSurfaceVariant,
                                )
                                if (gesturePointerEnabled && !Settings.canDrawOverlays(this@MainActivity)) {
                                    TextButton(onClick = { handleGesturePointerToggle(true) }) {
                                        Text("Allow display over other apps")
                                    }
                                }
                            }
                        }
                        status.sessions.forEach { item ->
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                    Text(item.purpose, style = MaterialTheme.typography.titleMedium)
                                    Text(item.requester, color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodyMedium)
                                    Text(item.caps.joinToString(" · ") { it.replace('_', ' ').replaceFirstChar(Char::uppercase) }, style = MaterialTheme.typography.bodySmall)
                                    if (item.caps.any { it in setOf("ui_tree", "touch", "text", "apps", "browser") }) Text(
                                        "This session can control the capabilities shown above. Stop the session here or in the console to revoke access.",
                                        color = colors.onSurfaceVariant,
                                        style = MaterialTheme.typography.bodySmall,
                                    )
                                    if (item.state == "pending_approval") Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(enabled = online && !busy, onClick = {
                                            execute {
                                                it.session(item.id, "approve")
                                                withContext(Dispatchers.Main) {
                                                    attending = item.id
                                                    if (item.caps.any { cap -> cap in setOf("ui_tree", "touch", "text", "apps", "browser") }) {
                                                        controlSessionId = item.id
                                                        ControlSessionService.start(this@MainActivity, item.id)
                                                    }
                                                }
                                            }
                                        }) { Text("Approve") }
                                        TextButton(enabled = online && !busy, onClick = { execute { it.session(item.id, "deny") } }) { Text("Deny") }
                                    } else if (item.state == "active") {
                                        if (sharing == item.id) {
                                            Text(
                                                if (hasFrames) "Screen frames are sending" else "Screen sharing is on · waiting for first frame…",
                                                color = colors.primary,
                                                style = MaterialTheme.typography.labelLarge,
                                            )
                                        }
                                        if (!sharingError.isNullOrBlank()) Text(
                                            sharingError.orEmpty(),
                                            color = colors.error,
                                            style = MaterialTheme.typography.bodySmall,
                                            modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite },
                                        )
                                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                            if (sharing != item.id && "screen" in item.caps) Button(enabled = online && !busy, onClick = { share(item.id) }) { Text("Share screen") }
                                            OutlinedButton(onClick = { stopSession(item.id) }) { Text("Stop", color = colors.error) }
                                        }
                                        if ("camera" in item.caps) {
                                            val active = cameraShare == item.id
                                            OutlinedButton(enabled = active || online && !busy && cameraShare == null, onClick = {
                                                if (active) SensorShareService.stop(this@MainActivity, MediaSource.CAMERA)
                                                else shareSensor(MediaSource.CAMERA, item.id)
                                            }) { Text(if (active) "Stop camera" else "Share camera") }
                                        }
                                        if ("microphone" in item.caps) {
                                            val active = microphoneShare == item.id
                                            OutlinedButton(enabled = active || online && !busy && microphoneShare == null, onClick = {
                                                if (active) SensorShareService.stop(this@MainActivity, MediaSource.MICROPHONE)
                                                else shareSensor(MediaSource.MICROPHONE, item.id)
                                            }) { Text(if (active) "Stop microphone" else "Share microphone") }
                                        }
                                        sensorError?.let { Text(it, color = colors.error, style = MaterialTheme.typography.bodySmall) }
                                    }
                                }
                            }
                        }
                        status.sensors.forEach { request ->
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                    Text(if (request.type == "camera") "Camera photo requested" else "${request.seconds}-second microphone clip requested", style = MaterialTheme.typography.titleMedium)
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(enabled = online && !busy && (if (request.type == "camera") cameraShare == null else microphoneShare == null), onClick = {
                                            if (request.type == "camera") takePhoto(request)
                                            else if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) record(request)
                                            else { micRequest = request; microphone.launch(Manifest.permission.RECORD_AUDIO) }
                                        }) { Text(if (request.type == "camera") "Open camera" else "Record clip") }
                                        TextButton(enabled = online && !busy, onClick = { execute { it.denySensor(request.id) } }) { Text("Deny") }
                                    }
                                }
                            }
                        }
                        TextButton(enabled = !busy, onClick = { confirmDisconnect = true }) { Text("Unpair phone") }
                    }
                    if (message.isNotBlank()) Text(message, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                }
              }
            }
            if (confirmDisconnect) AlertDialog(onDismissRequest = { confirmDisconnect = false },
                title = { Text("Unpair this phone?") }, text = { Text("Sharing stops and this phone will need a new pairing code.") },
                confirmButton = { TextButton(onClick = {
                    ScreenShareService.stop(this@MainActivity)
                    SensorShareService.stop(this@MainActivity, MediaSource.CAMERA)
                    SensorShareService.stop(this@MainActivity, MediaSource.MICROPHONE)
                    livePermission = null
                    SensorSharing.error.value = null
                    BackgroundMonitorService.setEnabled(this@MainActivity, false)
                    backgroundMonitorEnabled = false
                    ControlSessionService.stop(this@MainActivity)
                    controlSessionId = null
                    val previous = pairing
                    store.clear(); pairing = null; attending = null
                    online = false
                    status = Heartbeat(emptyList(), emptyList()); message = ""; confirmDisconnect = false
                    if (previous != null) lifecycleScope.launch(Dispatchers.IO) { runCatching { CompanionApi(previous).unpair() } }
                }) { Text("Unpair") } }, dismissButton = { TextButton(onClick = { confirmDisconnect = false }) { Text("Cancel") } })
        }
    }
}
