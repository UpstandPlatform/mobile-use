package org.mobileuse.companion

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import java.io.ByteArrayOutputStream
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive

private const val SAMPLE_RATE = 16000

suspend fun microphoneClip(context: Context, seconds: Int): ByteArray {
    check(context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
        "Grant microphone access before recording."
    }
    val bufferSize = AudioRecord.getMinBufferSize(SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
    check(bufferSize > 0) { "This phone cannot record a microphone clip." }
    val recorder = AudioRecord(MediaRecorder.AudioSource.MIC, SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO,
        AudioFormat.ENCODING_PCM_16BIT, bufferSize * 2)
    val bytes = ByteArray(SAMPLE_RATE * 2 * seconds.coerceIn(1, 15))
    try {
        check(recorder.state == AudioRecord.STATE_INITIALIZED) { "The microphone is unavailable." }
        recorder.startRecording()
        var offset = 0
        while (offset < bytes.size) {
            currentCoroutineContext().ensureActive()
            val count = recorder.read(bytes, offset, minOf(bufferSize, bytes.size - offset), AudioRecord.READ_BLOCKING)
            check(count > 0) { "The microphone stopped recording." }
            offset += count
        }
        return wav(bytes)
    } finally {
        if (recorder.recordingState == AudioRecord.RECORDSTATE_RECORDING) recorder.stop()
        recorder.release()
    }
}

private fun wav(pcm: ByteArray): ByteArray {
    val header = ByteBuffer.allocate(44).order(ByteOrder.LITTLE_ENDIAN)
    header.put("RIFF".toByteArray()).putInt(36 + pcm.size).put("WAVEfmt ".toByteArray())
    header.putInt(16).putShort(1).putShort(1).putInt(SAMPLE_RATE).putInt(SAMPLE_RATE * 2)
    header.putShort(2).putShort(16).put("data".toByteArray()).putInt(pcm.size)
    return header.array() + pcm
}

fun approvedPhoto(file: File): ByteArray {
    val dimensions = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeFile(file.path, dimensions)
    require(dimensions.outWidth > 0 && dimensions.outHeight > 0) { "The camera did not return a photo." }
    var sample = 1
    while (maxOf(dimensions.outWidth, dimensions.outHeight) / sample > 1600) sample *= 2
    val bitmap = BitmapFactory.decodeFile(file.path, BitmapFactory.Options().apply { inSampleSize = sample })
        ?: error("The camera did not return a photo.")
    try {
        return ByteArrayOutputStream().also { bitmap.compress(Bitmap.CompressFormat.JPEG, 82, it) }.toByteArray()
    } finally { bitmap.recycle() }
}
