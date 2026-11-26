package org.mobileuse.companion

import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.os.Bundle
import android.os.Handler
import android.view.Surface
import java.nio.ByteBuffer

internal const val VIDEO_FPS = 24

internal class AvcEncoder(
    val width: Int,
    val height: Int,
    bitRate: Int,
    handler: Handler,
    repeatLastFrame: Boolean = false,
    private val onFrame: (AvcEncoder, Long, ByteArray, Boolean) -> Unit,
    private val onError: (Throwable) -> Unit,
) : AutoCloseable {
    private val codec = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_VIDEO_AVC)
    val surface: Surface
    private var headers = ByteArray(0)
    private var closed = false

    init {
        var input: Surface? = null
        try {
            val profile = codec.codecInfo.getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC).profileLevels
            val format = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, width, height).apply {
                setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface)
                setInteger(MediaFormat.KEY_BIT_RATE, bitRate)
                setInteger(MediaFormat.KEY_FRAME_RATE, VIDEO_FPS)
                setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 1)
                setInteger(MediaFormat.KEY_MAX_B_FRAMES, 0)
                setFloat(MediaFormat.KEY_MAX_FPS_TO_ENCODER, VIDEO_FPS.toFloat())
                if (repeatLastFrame) setLong(MediaFormat.KEY_REPEAT_PREVIOUS_FRAME_AFTER, 200_000L)
                val constrained = MediaCodecInfo.CodecProfileLevel.AVCProfileConstrainedBaseline
                setInteger(MediaFormat.KEY_PROFILE, if (profile.any { it.profile == constrained }) constrained
                    else MediaCodecInfo.CodecProfileLevel.AVCProfileBaseline)
            }
            codec.setCallback(object : MediaCodec.Callback() {
                override fun onInputBufferAvailable(codec: MediaCodec, index: Int) = Unit

                override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {
                    headers = listOfNotNull(format.getByteBuffer("csd-0"), format.getByteBuffer("csd-1"))
                        .fold(ByteArray(0)) { combined, buffer -> combined + annexB(buffer.toByteArray()) }
                }

                override fun onOutputBufferAvailable(codec: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
                    if (closed) return
                    runCatching {
                        val data = try {
                            codec.getOutputBuffer(index)?.duplicate()?.apply {
                                position(info.offset)
                                limit(info.offset + info.size)
                            }?.toByteArray()
                        } finally { codec.releaseOutputBuffer(index, false) }
                        if (data == null || data.isEmpty()) return
                        if (info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0) {
                            if (headers.isEmpty()) headers = annexB(data)
                            return
                        }
                        val key = info.flags and MediaCodec.BUFFER_FLAG_KEY_FRAME != 0
                        val encoded = annexB(data)
                        onFrame(this@AvcEncoder, info.presentationTimeUs, if (key) headers + encoded else encoded, key)
                    }.onFailure(onError)
                }

                override fun onError(codec: MediaCodec, error: MediaCodec.CodecException) {
                    if (!closed) onError(error)
                }
            }, handler)
            codec.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            input = codec.createInputSurface()
            codec.start()
            surface = input
        } catch (error: Exception) {
            input?.release()
            codec.release()
            throw error
        }
    }

    fun requestKeyFrame() {
        if (!closed) runCatching { codec.setParameters(Bundle().apply { putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0) }) }
    }

    override fun close() {
        if (closed) return
        closed = true
        runCatching { codec.stop() }
        codec.release()
        surface.release()
    }
}

private fun ByteBuffer.toByteArray(): ByteArray = duplicate().let { buffer -> ByteArray(buffer.remaining()).also { buffer.get(it) } }

private val startCode = byteArrayOf(0, 0, 0, 1)

private fun annexB(data: ByteArray): ByteArray {
    if (data.size >= 4 && data[0] == 0.toByte() && data[1] == 0.toByte() &&
        (data[2] == 1.toByte() || data[2] == 0.toByte() && data[3] == 1.toByte())) return data
    val lengths = ByteBuffer.wrap(data)
    val units = ArrayList<ByteRange>()
    while (lengths.remaining() >= Int.SIZE_BYTES) {
        val count = lengths.int
        if (count <= 0 || count > lengths.remaining()) break
        val offset = lengths.position()
        units.add(ByteRange(offset, count))
        lengths.position(offset + count)
    }
    if (lengths.hasRemaining() || units.isEmpty()) return startCode + data
    val result = ByteBuffer.allocate(data.size)
    for ((offset, size) in units) result.put(startCode).put(data, offset, size)
    return result.array()
}

private data class ByteRange(val offset: Int, val size: Int)
