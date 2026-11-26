package org.mobileuse.companion

import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

@Composable
fun QrScannerScreen(
    onScanned: (PairingRequest) -> Unit,
    onCancel: () -> Unit,
) {
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current
    var hint by remember { mutableStateOf("Point the camera at the QR code shown in the mobile-use console or CLI.") }
    val handled = remember { AtomicBoolean(false) }
    val analyzerExecutor = remember { Executors.newSingleThreadExecutor() }
    val reader = remember {
        MultiFormatReader().apply {
            setHints(mapOf(DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE)))
        }
    }

    // The camera is bound to the Activity lifecycle, so it must be unbound explicitly when the
    // scanner leaves composition; otherwise it keeps running and feeds a shut-down executor.
    val cameraProvider = remember { AtomicReference<ProcessCameraProvider?>(null) }
    val disposed = remember { AtomicBoolean(false) }

    DisposableEffect(Unit) {
        onDispose {
            disposed.set(true)
            cameraProvider.getAndSet(null)?.let { runCatching { it.unbindAll() } }
            analyzerExecutor.shutdown()
        }
    }

    Box(Modifier.fillMaxSize()) {
        AndroidView(
            modifier = Modifier.fillMaxSize(),
            factory = { ctx ->
                val previewView = PreviewView(ctx)
                val future = ProcessCameraProvider.getInstance(ctx)
                future.addListener({
                    val provider = runCatching { future.get() }.getOrNull() ?: return@addListener
                    if (disposed.get()) return@addListener
                    cameraProvider.set(provider)
                    val preview = Preview.Builder().build().also {
                        it.surfaceProvider = previewView.surfaceProvider
                    }
                    val analysis = ImageAnalysis.Builder()
                        .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                        .build()
                    analysis.setAnalyzer(analyzerExecutor) { proxy ->
                        decodeQrFrame(proxy, reader) { raw ->
                            if (handled.get()) return@decodeQrFrame
                            runCatching { parsePairingUri(raw) }
                                .onSuccess { request ->
                                    if (handled.compareAndSet(false, true)) {
                                        ContextCompat.getMainExecutor(ctx).execute { onScanned(request) }
                                    }
                                }
                                .onFailure { err ->
                                    ContextCompat.getMainExecutor(ctx).execute {
                                        hint = err.message ?: "Scan the QR code from mobile-use."
                                    }
                                }
                        }
                    }
                    runCatching {
                        provider.unbindAll()
                        provider.bindToLifecycle(lifecycleOwner, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
                    }.onFailure {
                        hint = "Camera preview could not start on this device."
                    }
                }, ContextCompat.getMainExecutor(ctx))
                previewView
            }
        )

        // Subtle viewfinder overlay
        Canvas(
            modifier = Modifier
                .fillMaxSize()
                .graphicsLayer(compositingStrategy = CompositingStrategy.Offscreen)
        ) {
            drawRect(Color.Black.copy(alpha = 0.55f))
            val boxSize = minOf(size.width, size.height) * 0.65f
            val left = (size.width - boxSize) / 2f
            val top = (size.height - boxSize) / 2f
            drawRoundRect(
                color = Color.Transparent,
                topLeft = Offset(left, top),
                size = Size(boxSize, boxSize),
                cornerRadius = CornerRadius(24f, 24f),
                blendMode = BlendMode.Clear,
            )
            drawRoundRect(
                color = Color(0xFFD7ED99),
                topLeft = Offset(left, top),
                size = Size(boxSize, boxSize),
                cornerRadius = CornerRadius(24f, 24f),
                style = Stroke(width = 6f),
            )
        }

        Column(
            modifier = Modifier
                .fillMaxSize()
                .safeDrawingPadding()
                .padding(24.dp),
            verticalArrangement = Arrangement.SpaceBetween,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Surface(color = Color.Black.copy(alpha = 0.72f), shape = MaterialTheme.shapes.medium) {
                Text(
                    hint,
                    color = Color.White,
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
                )
            }
            Button(
                onClick = onCancel,
                colors = ButtonDefaults.buttonColors(containerColor = Color.White, contentColor = Color(0xFF232822)),
            ) {
                Text("Cancel")
            }
        }
    }
}

private fun decodeQrFrame(proxy: ImageProxy, reader: MultiFormatReader, onText: (String) -> Unit) {
    try {
        val plane = proxy.planes.firstOrNull() ?: return
        val width = proxy.width
        val height = proxy.height
        val rowStride = plane.rowStride
        val buffer = plane.buffer
        val data = ByteArray(width * height)
        if (rowStride == width) {
            buffer.get(data, 0, data.size)
        } else {
            val row = ByteArray(rowStride)
            for (y in 0 until height) {
                buffer.position(y * rowStride)
                val remaining = minOf(rowStride, buffer.remaining())
                buffer.get(row, 0, remaining)
                System.arraycopy(row, 0, data, y * width, width)
            }
        }
        val source = PlanarYUVLuminanceSource(data, width, height, 0, 0, width, height, false)
        val bitmap = BinaryBitmap(HybridBinarizer(source))
        val result = runCatching { reader.decodeWithState(bitmap) }.getOrNull()
        reader.reset()
        if (result != null && result.text.isNotBlank()) {
            onText(result.text)
        }
    } finally {
        proxy.close()
    }
}
