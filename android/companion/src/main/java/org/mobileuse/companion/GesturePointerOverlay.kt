package org.mobileuse.companion

import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PixelFormat
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.provider.Settings
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import org.json.JSONObject
import kotlin.math.min

internal data class PointerPoint(val x: Float, val y: Float)
internal data class PointerPath(val start: PointerPoint, val end: PointerPoint)
internal data class PointerGesture(val type: String, val durationMs: Int, val paths: List<PointerPath>)

internal object GesturePointerSettings {
    private const val PREFS = "gesture_pointer"
    private const val ENABLED = "enabled"

    fun enabled(context: Context): Boolean =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(ENABLED, false)

    fun setEnabled(context: Context, enabled: Boolean) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean(ENABLED, enabled).apply()
    }
}

internal fun parsePointerGesture(value: JSONObject): PointerGesture? {
    val type = value.optString("gesture")
    if (type !in setOf("tap", "double_tap", "long_press", "swipe", "drag", "scroll", "fling", "pinch", "rotate", "two_finger_tap", "multi_touch")) {
        return null
    }
    val rawPaths = value.optJSONArray("paths") ?: return null
    if (rawPaths.length() !in 1..5) return null
    val paths = (0 until rawPaths.length()).map { index ->
        val raw = rawPaths.optJSONObject(index) ?: return null
        val start = raw.optJSONObject("start")?.toPointerPoint() ?: return null
        val end = raw.optJSONObject("end")?.toPointerPoint() ?: return null
        PointerPath(start, end)
    }
    val duration = value.optInt("duration_ms", 350).coerceIn(1, 2000)
    return PointerGesture(type, duration, paths)
}

private fun JSONObject.toPointerPoint(): PointerPoint? {
    val x = optDouble("x", Double.NaN)
    val y = optDouble("y", Double.NaN)
    if (!x.isFinite() || !y.isFinite() || x < 0.0 || x >= 1.0 || y < 0.0 || y >= 1.0) return null
    return PointerPoint(x.toFloat(), y.toFloat())
}

/** Draws an opt-in pointer in a secure, full-screen, click-through Android overlay. */
internal class GesturePointerOverlay(context: Context) {
    private val appContext = context.applicationContext
    private val windowManager = context.getSystemService(WindowManager::class.java)
    private val mainHandler = Handler(Looper.getMainLooper())
    private val view = PointerView(context)
    private var attached = false
    private var closed = false

    fun show(gesture: PointerGesture) {
        mainHandler.post {
            if (closed || !Settings.canDrawOverlays(appContext)) return@post
            ensureAttached()
            view.show(gesture)
        }
    }

    fun close() {
        mainHandler.post {
            closed = true
            view.clear()
            if (attached) runCatching { windowManager.removeViewImmediate(view) }
            attached = false
        }
    }

    private fun ensureAttached() {
        if (attached) return
        val flags = WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
            WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE or
            WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN or
            WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS or
            WindowManager.LayoutParams.FLAG_SECURE
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            flags,
            PixelFormat.TRANSLUCENT,
        ).apply {
            gravity = Gravity.TOP or Gravity.START
            alpha = 0.7f
            layoutInDisplayCutoutMode = WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            setFitInsetsTypes(0)
            title = "mobile-use gesture pointer"
        }
        try {
            windowManager.addView(view, params)
            attached = true
        } catch (_: SecurityException) {
            // Permission may have been revoked in Settings while screen sharing was active.
        } catch (_: WindowManager.BadTokenException) {
            // Android can detach an overlay when the owning user or display changes.
        }
    }
}

private data class AnimatedPath(
    val startX: Float,
    val startY: Float,
    val endX: Float,
    val endY: Float,
    val beganAt: Long,
    val durationMs: Long,
    val color: Int,
    val tap: Boolean,
)

private class PointerView(context: Context) : View(context) {
    private val density = resources.displayMetrics.density
    private val colors = intArrayOf(0xFFB8F36B.toInt(), 0xFF77D8FF.toInt(), 0xFFFFC46B.toInt(), 0xFFC5A1FF.toInt(), 0xFFFF91AD.toInt())
    private val paths = mutableListOf<AnimatedPath>()
    private val ripples = mutableListOf<Pair<PointerPoint, Long>>()
    private val fill = Paint(Paint.ANTI_ALIAS_FLAG).apply { style = Paint.Style.FILL }
    private val stroke = Paint(Paint.ANTI_ALIAS_FLAG).apply { style = Paint.Style.STROKE; strokeCap = Paint.Cap.ROUND }
    private val trail = Path()

    init {
        isClickable = false
        isFocusable = false
        importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    fun show(gesture: PointerGesture) {
        val now = SystemClock.uptimeMillis()
        paths.clear()
        ripples.clear()
        gesture.paths.forEachIndexed { index, path ->
            paths += AnimatedPath(
                path.start.x, path.start.y, path.end.x, path.end.y,
                now, gesture.durationMs.toLong(), colors[index % colors.size],
                gesture.type in setOf("tap", "double_tap", "two_finger_tap"),
            )
        }
        invalidate()
    }

    fun clear() {
        paths.clear()
        ripples.clear()
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        if (width <= 0 || height <= 0) return
        val now = SystemClock.uptimeMillis()
        val remaining = mutableListOf<AnimatedPath>()
        paths.forEach { item ->
            val progress = ((now - item.beganAt).toFloat() / item.durationMs).coerceIn(0f, 1f)
            val x = (item.startX + (item.endX - item.startX) * progress) * width
            val y = (item.startY + (item.endY - item.startY) * progress) * height
            val startX = item.startX * width
            val startY = item.startY * height
            drawTrail(canvas, item, startX, startY, x, y, progress)
            drawCursor(canvas, x, y, item.color, progress)
            if (item.tap && progress >= 1f) {
                if (ripples.none { it.first.x == item.endX && it.first.y == item.endY && it.second == item.beganAt }) {
                    ripples += PointerPoint(item.endX, item.endY) to (item.beganAt + item.durationMs)
                }
            }
            if (now < item.beganAt + item.durationMs + if (item.tap) 420 else 180) remaining += item
        }
        paths.clear()
        paths.addAll(remaining)
        drawRipples(canvas, now)
        if (paths.isNotEmpty() || ripples.isNotEmpty()) postInvalidateOnAnimation()
    }

    private fun drawTrail(canvas: Canvas, item: AnimatedPath, startX: Float, startY: Float, x: Float, y: Float, progress: Float) {
        if (progress < 0.02f || startX == x && startY == y) return
        trail.reset()
        trail.moveTo(startX, startY)
        trail.lineTo(x, y)
        stroke.color = item.color
        stroke.alpha = 48
        stroke.strokeWidth = 3.5f * density
        canvas.drawPath(trail, stroke)
    }

    private fun drawCursor(canvas: Canvas, x: Float, y: Float, color: Int, progress: Float) {
        val radius = 13f * density
        fill.color = color
        fill.alpha = 42
        canvas.drawCircle(x, y, radius * (1.25f - progress * 0.12f), fill)
        fill.alpha = 235
        val arrow = Path().apply {
            moveTo(x, y)
            lineTo(x + 5.5f * density, y + 18f * density)
            lineTo(x + 9f * density, y + 11f * density)
            lineTo(x + 16f * density, y + 10f * density)
            close()
        }
        fill.color = color
        canvas.drawPath(arrow, fill)
        stroke.color = 0xFF17211A.toInt()
        stroke.alpha = 235
        stroke.strokeWidth = 1.8f * density
        canvas.drawPath(arrow, stroke)
        fill.color = 0xFFFFFFFF.toInt()
        fill.alpha = 235
        canvas.drawCircle(x, y, 2.2f * density, fill)
    }

    private fun drawRipples(canvas: Canvas, now: Long) {
        val keep = mutableListOf<Pair<PointerPoint, Long>>()
        ripples.forEach { (point, startedAt) ->
            val age = now - startedAt
            val rippleProgress = ((age - 150L).toFloat() / 420f).coerceIn(0f, 1f)
            if (age < 650L) {
                val alpha = ((1f - rippleProgress) * 220f).toInt()
                stroke.color = 0xFFB8F36B.toInt()
                stroke.alpha = alpha
                stroke.strokeWidth = 2f * density
                canvas.drawCircle(point.x * width, point.y * height, (8f + rippleProgress * 26f) * density, stroke)
                keep += point to startedAt
            }
        }
        ripples.clear()
        ripples.addAll(keep)
    }
}
