package org.mobileuse.companion

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.media.projection.MediaProjectionManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
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
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import androidx.core.view.WindowCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    private lateinit var store: PairingStore
    private var pairing by mutableStateOf<Pairing?>(null)
    private var status by mutableStateOf(Heartbeat(emptyList(), emptyList()))
    private var message by mutableStateOf("")
    private var attending by mutableStateOf<String?>(null)
    private var busy by mutableStateOf(false)
    private var online by mutableStateOf(false)
    private var projectionId: String? = null
    private var micRequest: SensorRequest? = null
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
    private val microphone = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val request = micRequest ?: return@registerForActivityResult
        micRequest = null
        if (granted) record(request) else execute { it.denySensor(request.id) }
    }
    private val camera = registerForActivityResult(ActivityResultContracts.TakePicture()) { approved ->
        val request = photoRequest
        val file = photoFile
        photoRequest = null
        photoFile = null
        if (request == null || file == null) return@registerForActivityResult
        execute {
            try {
                if (approved) it.upload(request.id, approvedPhoto(file), "image/jpeg") else it.denySensor(request.id)
            } finally { file.delete() }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.getInsetsController(window, window.decorView).apply {
            isAppearanceLightStatusBars = true
            isAppearanceLightNavigationBars = true
        }
        store = PairingStore(this)
        pairing = store.read()
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                while (true) {
                    pairing?.let { current ->
                        try {
                            val value = withContext(Dispatchers.IO) { CompanionApi(current).heartbeat(attending) }
                            if (pairing == current) {
                                status = value
                                online = true
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

    override fun onStop() {
        micJob?.cancel()
        super.onStop()
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

    private fun record(request: SensorRequest) {
        val current = pairing ?: return
        if (busy) return
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
    }

    private fun takePhoto(request: SensorRequest) {
        val folder = File(cacheDir, "approved-photos").apply { mkdirs() }
        val file = File.createTempFile("photo-", ".jpg", folder)
        photoRequest = request
        photoFile = file
        try { camera.launch(FileProvider.getUriForFile(this, "$packageName.photos", file)) }
        catch (_: Exception) { file.delete(); photoFile = null; photoRequest = null; message = "A system camera app is unavailable." }
    }

    private fun stopSession(id: String) {
        val current = pairing ?: return
        attending = null
        projectionId = null
        micRequest = null
        micJob?.cancel()
        ScreenShareService.stop(this)
        lifecycleScope.launch {
            try {
                withContext(Dispatchers.IO) { CompanionApi(current).session(id, "end") }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { message = "Sharing stopped on this phone. The console will update when it reconnects." }
        }
    }

    @Composable
    private fun CompanionUI() {
        val sharing by Sharing.session.collectAsState()
        var origin by rememberSaveable { mutableStateOf("") }
        var code by remember { mutableStateOf("") }
        var confirmDisconnect by remember { mutableStateOf(false) }
        val colors = lightColorScheme(primary = Color(0xFF405B30), primaryContainer = Color(0xFFD7ED99),
            onPrimaryContainer = Color(0xFF243419), background = Color(0xFFF5F5EF), surface = Color(0xFFFDFDF8))
        MaterialTheme(colorScheme = colors) {
            Surface(modifier = Modifier.fillMaxSize(), color = colors.background) {
              Box(Modifier.fillMaxSize().safeDrawingPadding().imePadding(), contentAlignment = Alignment.TopCenter) {
                Column(Modifier.widthIn(max = 480.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalAlignment = Alignment.CenterVertically) {
                        Image(painterResource(R.drawable.mobile_use_icon), null, Modifier.size(36.dp))
                        Text("mobile-use", style = MaterialTheme.typography.titleMedium)
                    }
                    if (pairing == null) {
                        Text("Pair your phone", style = MaterialTheme.typography.headlineSmall)
                        Text("In the console, choose Pair Android to get the server address and code.", color = colors.onSurfaceVariant)
                        OutlinedTextField(origin, { origin = it }, label = { Text("Server address") }, placeholder = { Text("https://") }, singleLine = true,
                            enabled = !busy, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next), modifier = Modifier.fillMaxWidth())
                        OutlinedTextField(code, { code = it.trim() }, label = { Text("Pairing code") }, singleLine = true,
                            enabled = !busy, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, autoCorrectEnabled = false, imeAction = ImeAction.Done),
                            modifier = Modifier.fillMaxWidth())
                        Button(enabled = !busy && origin.isNotBlank() && code.isNotBlank(), modifier = Modifier.fillMaxWidth(), onClick = {
                            busy = true; message = ""
                            lifecycleScope.launch {
                                try {
                                    pairing = withContext(Dispatchers.IO) {
                                        CompanionApi.enroll(origin, code.trim(), Build.MODEL).also(store::save)
                                    }
                                    code = ""
                                } catch (cancelled: CancellationException) { throw cancelled }
                                catch (error: ApiFailure) { message = error.message.orEmpty() }
                                catch (_: Exception) { message = "Check the server address, certificate and pairing code." }
                                finally { busy = false }
                            }
                        }) { Text(if (busy) "Pairing…" else "Pair phone") }
                    } else {
                        Text(if (online) "Connected" else "Waiting for connection", style = MaterialTheme.typography.headlineSmall)
                        if (status.sessions.isEmpty()) Text("Sharing requests from your console will appear here.", color = colors.onSurfaceVariant)
                        status.sessions.forEach { item ->
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                    Text(item.purpose, style = MaterialTheme.typography.titleMedium)
                                    Text(item.requester, color = colors.onSurfaceVariant, style = MaterialTheme.typography.bodyMedium)
                                    Text(item.caps.joinToString(" · ") { it.replace('_', ' ').replaceFirstChar(Char::uppercase) }, style = MaterialTheme.typography.bodySmall)
                                    if (item.state == "pending_approval") Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(enabled = online && !busy, onClick = { execute { it.session(item.id, "approve"); withContext(Dispatchers.Main) { attending = item.id } } }) { Text("Approve") }
                                        TextButton(enabled = online && !busy, onClick = { execute { it.session(item.id, "deny") } }) { Text("Deny") }
                                    } else if (item.state == "active") {
                                        if (sharing == item.id) Text("Sharing screen", color = colors.primary, style = MaterialTheme.typography.labelLarge)
                                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                            if (sharing != item.id && "screen" in item.caps) Button(enabled = online && !busy, onClick = { share(item.id) }) { Text("Share screen") }
                                            OutlinedButton(onClick = { stopSession(item.id) }) { Text("Stop", color = colors.error) }
                                        }
                                    }
                                }
                            }
                        }
                        status.sensors.forEach { request ->
                            OutlinedCard(Modifier.fillMaxWidth()) {
                                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                    Text(if (request.type == "camera") "Camera photo requested" else "${request.seconds}-second microphone clip requested", style = MaterialTheme.typography.titleMedium)
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                        Button(enabled = online && !busy, onClick = {
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
                    val previous = pairing
                    store.clear(); pairing = null; attending = null
                    online = false
                    status = Heartbeat(emptyList(), emptyList()); message = ""; confirmDisconnect = false
                    if (previous != null) lifecycleScope.launch(Dispatchers.IO) { runCatching { CompanionApi(previous).unpair() } }
                }) { Text("Unpair") } }, dismissButton = { TextButton(onClick = { confirmDisconnect = false }) { Text("Cancel") } })
        }
    }
}
