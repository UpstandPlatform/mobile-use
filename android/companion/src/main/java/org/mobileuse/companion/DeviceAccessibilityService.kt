package org.mobileuse.companion

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.AccessibilityServiceInfo
import android.accessibilityservice.GestureDescription
import android.app.KeyguardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Path
import android.graphics.Rect
import android.net.Uri
import android.os.Bundle
import android.os.PowerManager
import android.view.WindowManager
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import org.json.JSONArray
import org.json.JSONObject
import java.security.MessageDigest
import java.net.URI
import java.time.Instant
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

internal object PhoneControlCapabilities {
    fun current(context: Context): Set<String> = linkedSetOf("apps").apply {
        if (DeviceAccessibilityService.isConnected()) {
            addAll(listOf("ui_tree", "touch", "text", "browser", "multi_touch"))
        }
    }
}

internal class DeviceAccessibilityService : AccessibilityService() {
    override fun onServiceConnected() {
        super.onServiceConnected()
        serviceInfo = serviceInfo.apply {
            eventTypes = AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED or
                AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED or AccessibilityEvent.TYPE_WINDOWS_CHANGED
            feedbackType = AccessibilityServiceInfo.FEEDBACK_GENERIC
            flags = AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS or
                AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS or
                AccessibilityServiceInfo.FLAG_INCLUDE_NOT_IMPORTANT_VIEWS
            notificationTimeout = 100
        }
        connected = this
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) = Unit
    override fun onInterrupt() = Unit

    override fun onUnbind(intent: Intent?): Boolean {
        if (connected === this) connected = null
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        if (connected === this) connected = null
        super.onDestroy()
    }

    internal data class Snapshot(
        val width: Int,
        val height: Int,
        val appId: String,
        val nodes: List<JSONObject>,
        val nodeRefs: Map<String, AccessibilityNodeInfo>,
    )

    internal fun snapshot(): Snapshot = onMain {
        ensureUnlocked()
        val bounds = getSystemService(WindowManager::class.java).maximumWindowMetrics.bounds
        val root = rootInActiveWindow ?: throw ControlFault("UI_UNAVAILABLE", "Android has no accessible foreground window. Unlock the phone and open the screen you want to control.")
        val nodes = ArrayList<JSONObject>(128)
        val refs = LinkedHashMap<String, AccessibilityNodeInfo>()
        appendNode(root, "", 0, nodes, refs, 0)
        Snapshot(bounds.width().coerceAtLeast(1), bounds.height().coerceAtLeast(1), root.packageName?.toString().orEmpty(), nodes, refs)
    }

    internal fun perform(action: JSONObject, expectedSnapshot: Snapshot? = null): Boolean {
        val outcome = onMain {
            ensureUnlocked()
            val type = action.optString("type")
            val nodeId = action.optString("node_id")
            if (nodeId.isNotEmpty()) {
                val current = snapshot()
                val node = current.nodeRefs[nodeId]
                    ?: throw ControlFault("STALE_REFERENCE", "The target moved or disappeared. Observe the phone and choose it again.")
                val saved = expectedSnapshot?.nodeRefs?.get(nodeId)
                if (saved != null) {
                    val before = Rect().also(saved::getBoundsInScreen)
                    val now = Rect().also(node::getBoundsInScreen)
                    if (before != now) throw ControlFault("STALE_REFERENCE", "The target moved. Observe the phone and choose it again.")
                }
                when (type) {
                    "tap" -> {
                        if (node.performAction(AccessibilityNodeInfo.ACTION_CLICK)) return@onMain true
                        return@onMain dispatchGesture(action, current.width, current.height, node)
                    }
                    "double_tap" -> {
                        if (!node.performAction(AccessibilityNodeInfo.ACTION_CLICK)) throw ControlFault("ACTION_REJECTED", "The selected item could not be tapped.")
                        Thread.sleep(90)
                        return@onMain actionResult(node.performAction(AccessibilityNodeInfo.ACTION_CLICK), "The selected item could not be tapped twice.")
                    }
                    "scroll" -> {
                        val forward = action.optString("direction") in setOf("down", "right")
                        val supported = node.performAction(if (forward) AccessibilityNodeInfo.ACTION_SCROLL_FORWARD else AccessibilityNodeInfo.ACTION_SCROLL_BACKWARD)
                        if (supported) return@onMain true
                        return@onMain dispatchGesture(action, current.width, current.height, node)
                    }
                    "long_press" -> return@onMain dispatchGesture(action, current.width, current.height, node)
                }
            }
            when (type) {
                "type" -> setFocusedText(action.optString("text"))
                "key" -> pressKey(action.optString("key"))
                else -> {
                    val current = expectedSnapshot ?: snapshot()
                    dispatchGesture(action, current.width, current.height, null)
                }
            }
        }
        return if (outcome is GestureTicket) awaitGesture(outcome) else outcome as Boolean
    }

    internal fun performBrowserSelector(selector: String, type: String, text: String, deltaY: Int, expectedDocument: String) {
        val outcome = onMain {
            val current = snapshot()
            val doc = browserDocumentId(current)
            if (doc != expectedDocument) throw ControlFault("STALE_REFERENCE", "The browser page changed. Observe the page before acting again.")
            val normalized = selector.trim().removePrefix("text=").removePrefix("#").trim('"', '\'')
            val node = current.nodes.asSequence().filter { it.optBoolean("visible", true) }.firstNotNullOfOrNull { item ->
                val label = item.optString("label")
                val resource = item.optString("resource_id").substringAfterLast('/')
                if (label.equals(normalized, true) || label.contains(normalized, true) || resource.equals(normalized, true)) current.nodeRefs[item.optString("id")] else null
            }
                ?: throw ControlFault("ELEMENT_NOT_FOUND", "The requested browser control is not visible in Android's accessibility tree.")
            when (type) {
                "click" -> actionResult(node.performAction(AccessibilityNodeInfo.ACTION_CLICK), "The browser control could not be clicked.")
                "fill" -> setNodeText(node, text)
                "scroll" -> {
                    val action = JSONObject().put("type", "scroll").put("direction", if (deltaY >= 0) "down" else "up")
                    dispatchGesture(action, current.width, current.height, node)
                }
                else -> throw ControlFault("UNSUPPORTED", "This browser action is not supported on Android.")
            }
        }
        if (outcome is GestureTicket) awaitGesture(outcome)
    }

    private fun appendNode(
        node: AccessibilityNodeInfo,
        parent: String,
        index: Int,
        output: MutableList<JSONObject>,
        refs: MutableMap<String, AccessibilityNodeInfo>,
        depth: Int,
    ): String {
        if (output.size >= 900 || depth > 28) return parent
        val bounds = Rect().also(node::getBoundsInScreen)
        val protected = node.isPassword
        val label = if (protected) "" else bounded(node.text?.toString().orEmpty().ifBlank { node.contentDescription?.toString().orEmpty() }, 256)
        val hint = if (protected) "" else bounded(node.hintText?.toString().orEmpty(), 256)
        val role = bounded(node.className?.toString().orEmpty(), 128)
        val resourceId = bounded(node.viewIdResourceName.orEmpty(), 256)
        val key = "$parent/$index/$role/$label/$resourceId/${bounds.left},${bounds.top},${bounds.right},${bounds.bottom}"
        val id = digestId(key)
        val item = JSONObject()
            .put("id", id)
            .put("parent_id", parent)
            .put("role", role)
            .put("label", label)
            .put("hint", hint)
            .put("resource_id", resourceId)
            .put("bounds", JSONObject().put("x", bounds.left).put("y", bounds.top)
                .put("width", bounds.width().coerceAtLeast(0)).put("height", bounds.height().coerceAtLeast(0)))
            .put("enabled", node.isEnabled)
            .put("visible", node.isVisibleToUser)
            .put("clickable", node.isClickable)
            .put("long_clickable", node.isLongClickable)
            .put("focusable", node.isFocusable)
            .put("editable", node.isEditable)
            .put("checkable", node.isCheckable)
            .put("checked", node.isChecked)
            .put("selected", node.isSelected)
            .put("scrollable", node.isScrollable)
            .put("protected", protected)
            .put("focused", node.isFocused)
        output += item
        refs[id] = node
        for (childIndex in 0 until node.childCount) {
            val child = node.getChild(childIndex) ?: continue
            appendNode(child, id, childIndex, output, refs, depth + 1)
        }
        return id
    }

    private fun dispatchGesture(action: JSONObject, width: Int, height: Int, target: AccessibilityNodeInfo?): GestureTicket {
        val rawPaths = action.optJSONArray("paths")
        val paths = if (rawPaths != null && rawPaths.length() > 0) {
            (0 until rawPaths.length()).map { index ->
                val item = rawPaths.getJSONObject(index)
                item.getJSONObject("start").point() to item.getJSONObject("end").point()
            }
        } else {
            val rect = target?.let { Rect().also(it::getBoundsInScreen) }
            val points = if (action.optString("type") == "scroll" && rect != null && action.optJSONObject("point") == null) {
                scrollPoints(rect, width, height, action.optString("direction"))
            } else {
                val start = action.optJSONObject("point")?.point() ?: rect?.let {
                    (it.centerX().toDouble() / width).coerceIn(0.0, 0.999999) to
                        (it.centerY().toDouble() / height).coerceIn(0.0, 0.999999)
                } ?: throw ControlFault("INVALID_ACTION", "A screen point is required for this gesture.")
                start to (action.optJSONObject("end")?.point() ?: start)
            }
            mutableListOf(points).apply {
                action.optJSONObject("second_point")?.let { second ->
                    add(second.point() to (action.optJSONObject("second_end")?.point() ?: second.point()))
                }
            }
        }
        if (paths.isEmpty() || paths.size > 5) throw ControlFault("INVALID_ACTION", "Send one to five gesture paths.")
        val duration = action.optInt("duration_ms", 350).coerceIn(1, 3000)
        val builder = GestureDescription.Builder()
        paths.forEach { (start, end) ->
            val p = Path().apply {
                moveTo((start.first * width).toFloat(), (start.second * height).toFloat())
                lineTo((end.first * width).toFloat(), (end.second * height).toFloat())
            }
            builder.addStroke(GestureDescription.StrokeDescription(p, 0, duration.toLong()))
        }
        val ticket = GestureTicket(CountDownLatch(1))
        val accepted = dispatchGesture(builder.build(), object : GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) { ticket.succeeded = true; ticket.completed.countDown() }
            override fun onCancelled(gestureDescription: GestureDescription?) { ticket.completed.countDown() }
        }, null)
        if (!accepted) throw ControlFault("ACTION_REJECTED", "Android rejected the requested gesture.")
        return ticket
    }

    private fun awaitGesture(ticket: GestureTicket): Boolean {
        if (!ticket.completed.await(4, TimeUnit.SECONDS) || !ticket.succeeded) {
            throw ControlFault("ACTION_REJECTED", "Android did not complete the requested gesture.")
        }
        return true
    }

    private fun scrollPoints(bounds: Rect, width: Int, height: Int, direction: String): Pair<Pair<Double, Double>, Pair<Double, Double>> {
        val left = bounds.left.coerceIn(0, width - 1)
        val right = bounds.right.coerceIn(left + 1, width)
        val top = bounds.top.coerceIn(0, height - 1)
        val bottom = bounds.bottom.coerceIn(top + 1, height)
        val centerX = (left + right) / 2.0
        val centerY = (top + bottom) / 2.0
        val dx = ((right - left) * 0.3).coerceAtLeast(1.0)
        val dy = ((bottom - top) * 0.3).coerceAtLeast(1.0)
        val (fromX, fromY, toX, toY) = when (direction) {
            "up" -> arrayOf(centerX, (centerY + dy).coerceAtMost((bottom - 1).toDouble()), centerX, (centerY - dy).coerceAtLeast(top.toDouble()))
            "down" -> arrayOf(centerX, (centerY - dy).coerceAtLeast(top.toDouble()), centerX, (centerY + dy).coerceAtMost((bottom - 1).toDouble()))
            "left" -> arrayOf((centerX + dx).coerceAtMost((right - 1).toDouble()), centerY, (centerX - dx).coerceAtLeast(left.toDouble()), centerY)
            "right" -> arrayOf((centerX - dx).coerceAtLeast(left.toDouble()), centerY, (centerX + dx).coerceAtMost((right - 1).toDouble()), centerY)
            else -> throw ControlFault("INVALID_ACTION", "Choose up, down, left, or right for a scroll.")
        }
        return (fromX / width to fromY / height) to (toX / width to toY / height)
    }

    private fun setFocusedText(text: String): Boolean {
        if (text.isEmpty() || text.length > 4096 || text.contains('\u0000')) throw ControlFault("INVALID_TEXT", "Send between 1 and 4096 characters.")
        val root = rootInActiveWindow ?: throw ControlFault("UI_UNAVAILABLE", "There is no accessible input field on the current screen.")
        val target = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: throw ControlFault("FOCUS_REQUIRED", "Focus a text field before typing.")
        if (target.isPassword) throw ControlFault("PROTECTED_FIELD", "Text input is disabled for password fields.")
        if (!target.isEditable) throw ControlFault("NOT_EDITABLE", "The focused item is not an editable field.")
        val existing = target.text?.toString().orEmpty()
        val edit = insertAtSelection(existing, target.textSelectionStart, target.textSelectionEnd, text)
        if (edit.text.length > 4096) throw ControlFault("INVALID_TEXT", "The resulting field value exceeds the 4096-character limit.")
        if (!setNodeText(target, edit.text)) return false
        val args = Bundle().apply {
            putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_START_INT, edit.cursor)
            putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_END_INT, edit.cursor)
        }
        target.performAction(AccessibilityNodeInfo.ACTION_SET_SELECTION, args)
        return true
    }

    private fun setNodeText(node: AccessibilityNodeInfo, text: String): Boolean {
        if (node.isPassword) throw ControlFault("PROTECTED_FIELD", "Text input is disabled for password fields.")
        if (!node.isEditable) throw ControlFault("NOT_EDITABLE", "The selected item is not an editable field.")
        val args = Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text) }
        return actionResult(node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args), "Android rejected the text input.")
    }

    private fun pressKey(key: String): Boolean {
        return when (key) {
            "home" -> globalAction(GLOBAL_ACTION_HOME)
            "back", "escape" -> globalAction(GLOBAL_ACTION_BACK)
            "overview" -> globalAction(GLOBAL_ACTION_RECENTS)
            "notification_shade" -> globalAction(GLOBAL_ACTION_NOTIFICATIONS)
            "quick_settings" -> globalAction(GLOBAL_ACTION_QUICK_SETTINGS)
            "enter" -> throw ControlFault("UNSUPPORTED_KEY", "Android does not expose a reliable Enter key through accessibility. Use the visible keyboard action button when the app requires submission.")
            "backspace" -> {
                val root = rootInActiveWindow ?: throw ControlFault("UI_UNAVAILABLE", "There is no active screen.")
                val target = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
                    ?: throw ControlFault("FOCUS_REQUIRED", "Focus an input field before pressing Backspace.")
                if (target.isPassword) throw ControlFault("PROTECTED_FIELD", "Keyboard actions are disabled for password fields.")
                val current = target.text?.toString().orEmpty()
                val edit = backspaceAtSelection(current, target.textSelectionStart, target.textSelectionEnd)
                if (edit.text == current) true else {
                    if (!setNodeText(target, edit.text)) return false
                    val args = Bundle().apply {
                        putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_START_INT, edit.cursor)
                        putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_END_INT, edit.cursor)
                    }
                    target.performAction(AccessibilityNodeInfo.ACTION_SET_SELECTION, args)
                    true
                }
            }
            "tab", "menu", "dpad_up", "dpad_down", "dpad_left", "dpad_right", "volume_up", "volume_down", "volume_mute" ->
                throw ControlFault("UNSUPPORTED_KEY", "Android accessibility cannot inject the $key key on this device. Use a touch gesture or Android navigation action.")
            else -> throw ControlFault("INVALID_KEY", "This navigation key is not supported.")
        }
    }

    private fun globalAction(action: Int): Boolean = actionResult(performGlobalAction(action), "Android rejected the navigation action.")

    private fun ensureUnlocked() {
        val keyguard = getSystemService(KeyguardManager::class.java)
        val power = getSystemService(PowerManager::class.java)
        if (keyguard.isKeyguardLocked || !power.isInteractive) {
            throw ControlFault("OWNER_ACTION_REQUIRED", "Unlock the phone before the agent reads or controls its screen.")
        }
    }

    private fun <T> onMain(action: () -> T): T {
        if (android.os.Looper.myLooper() == android.os.Looper.getMainLooper()) return action()
        val latch = CountDownLatch(1)
        var value: T? = null
        var failure: Throwable? = null
        android.os.Handler(android.os.Looper.getMainLooper()).post {
            try { value = action() } catch (error: Throwable) { failure = error } finally { latch.countDown() }
        }
        if (!latch.await(8, TimeUnit.SECONDS)) throw ControlFault("CONTROL_TIMEOUT", "Android accessibility did not respond in time.")
        failure?.let { throw it }
        @Suppress("UNCHECKED_CAST")
        return value as T
    }

    companion object {
        @Volatile private var connected: DeviceAccessibilityService? = null
        fun current(): DeviceAccessibilityService? = connected
        fun isConnected(): Boolean = connected != null
    }
}

internal object PhoneControl {
    fun execute(context: Context, dispatch: JSONObject): JSONObject {
        val op = dispatch.optJSONObject("operation") ?: return failure("", "INVALID_OPERATION", "The command is missing its operation.")
        val id = op.optString("id")
        return try {
            when (op.optString("kind")) {
                "apps_list" -> {
                    val apps = installedApps(context)
                    success(id).put("apps", apps.first).put("installed_apps", apps.second)
                }
                "app_launch" -> { launchApp(context, op.optString("app_id")); success(id) }
                "app_terminate" -> throw ControlFault("UNSUPPORTED", "Android does not let a companion force-stop another app. Use Android's app switcher or connect ADB for force-stop.")
                "observe" -> {
                    val service = accessibilityRequired()
                    val request = op.optJSONObject("observe") ?: JSONObject()
                    val waitFor = request.optString("wait_for")
                    if (waitFor.isNotEmpty()) waitForNode(service, waitFor, request.optInt("timeout_ms", 2000).coerceIn(1, 8000))
                    val snapshot = service.snapshot()
                    success(id).put("observation", observation(snapshot))
                }
                "act" -> {
                    val service = accessibilityRequired()
                    val action = op.optJSONObject("action") ?: throw ControlFault("INVALID_ACTION", "The command is missing its action.")
                    val expected = action.optString("node_id").takeIf { it.isNotEmpty() }?.let { service.snapshot() }
                    if (!service.perform(action, expected)) throw ControlFault("ACTION_REJECTED", "Android did not complete the requested action.")
                    success(id)
                }
                "browser_contexts" -> success(id).put("contexts", browserContexts(context))
                "browser_observe" -> success(id).put("page", browserPage(accessibilityRequired()))
                "browser_act" -> {
                    val request = op.optJSONObject("browser") ?: JSONObject()
                    val service = accessibilityRequired()
                    when (request.optString("type")) {
                        "navigate" -> openWebPage(context, request.optString("url"), request.optString("context_id"))
                        else -> {
                            val page = browserPage(service)
                            if (page.optString("document_id") != request.optString("document_id")) {
                                throw ControlFault("STALE_REFERENCE", "The browser page changed. Observe it before acting again.")
                            }
                            service.performBrowserSelector(
                                request.optString("selector"), request.optString("type"), request.optString("text"),
                                request.optInt("delta_y"), request.optString("document_id"),
                            )
                        }
                    }
                    success(id)
                }
                "app_install", "app_reset" -> throw ControlFault("UNSUPPORTED", "APK install and app-data reset require the approved ADB app-management connector on Android.")
                else -> throw ControlFault("UNSUPPORTED", "This operation is not implemented by the Android companion.")
            }
        } catch (fault: ControlFault) {
            failure(id, fault.code, fault.message.orEmpty())
        } catch (_: Exception) {
            failure(id, "CONTROL_FAILED", "Android could not complete the requested action.")
        }
    }

    private fun accessibilityRequired(): DeviceAccessibilityService = DeviceAccessibilityService.current()
        ?: throw ControlFault("ACCESSIBILITY_REQUIRED", "Enable mobile-use accessibility control in Android Settings, then retry this operation.")

    private fun installedApps(context: Context): Pair<JSONArray, JSONArray> {
        val packageManager = context.packageManager
        val applications = packageManager.getInstalledApplications(PackageManager.ApplicationInfoFlags.of(0))
            .asSequence()
            .filter { it.packageName.contains('.') && packageManager.getLaunchIntentForPackage(it.packageName) != null }
            .distinctBy { it.packageName }
            .sortedBy { it.packageName }
            .take(1000)
            .toList()
        val ids = JSONArray()
        val details = JSONArray()
        applications.forEach { app ->
            ids.put(app.packageName)
            val name = runCatching { packageManager.getApplicationLabel(app).toString() }.getOrDefault(app.packageName)
            details.put(JSONObject().put("id", app.packageName).put("name", name.take(128)).put("launchable", true))
        }
        return ids to details
    }

    private fun launchApp(context: Context, packageId: String) {
        ensureDeviceUnlocked(context)
        if (!packageId.matches(Regex("[A-Za-z0-9_]+(\\.[A-Za-z0-9_]+)+"))) throw ControlFault("INVALID_APP", "Choose an installed Android package ID.")
        val intent = context.packageManager.getLaunchIntentForPackage(packageId)
            ?: throw ControlFault("APP_NOT_FOUND", "No launchable app with that package ID is installed.")
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        context.startActivity(intent)
    }

    private fun browserContexts(context: Context): JSONArray {
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse("https://example.invalid"))
            .addCategory(Intent.CATEGORY_BROWSABLE)
        val handlers = context.packageManager.queryIntentActivities(intent, PackageManager.ResolveInfoFlags.of(PackageManager.MATCH_DEFAULT_ONLY.toLong()))
        val result = JSONArray()
        handlers.map { it.activityInfo.packageName }.distinct().take(32).forEach { packageId ->
            val label = runCatching { context.packageManager.getApplicationLabel(context.packageManager.getApplicationInfo(packageId, PackageManager.ApplicationInfoFlags.of(0))).toString() }.getOrDefault(packageId)
            result.put(JSONObject().put("id", packageId).put("title", label))
        }
        return result
    }

    private fun openWebPage(context: Context, url: String, packageId: String) {
        ensureDeviceUnlocked(context)
        val uri = runCatching { Uri.parse(url) }.getOrNull()
        if (uri == null || uri.host.isNullOrBlank() || uri.scheme !in setOf("https", "http") || uri.userInfo != null) {
            throw ControlFault("INVALID_URL", "Open a valid HTTP or HTTPS browser URL.")
        }
        val intent = Intent(Intent.ACTION_VIEW, uri).addCategory(Intent.CATEGORY_BROWSABLE).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (packageId.isNotEmpty()) intent.setPackage(packageId)
        if (intent.resolveActivity(context.packageManager) == null) throw ControlFault("BROWSER_UNAVAILABLE", "No installed Android browser can open this URL.")
        context.startActivity(intent)
    }

    private fun ensureDeviceUnlocked(context: Context) {
        val keyguard = context.getSystemService(KeyguardManager::class.java)
        val power = context.getSystemService(PowerManager::class.java)
        if (keyguard.isKeyguardLocked || !power.isInteractive) {
            throw ControlFault("OWNER_ACTION_REQUIRED", "Unlock the phone before opening an app or browser page.")
        }
    }

    private fun browserPage(service: DeviceAccessibilityService): JSONObject {
        val snapshot = service.snapshot()
        val contexts = browserContexts(service)
        val packageId = snapshot.appId
        if ((0 until contexts.length()).none { contexts.getJSONObject(it).optString("id") == packageId }) {
            throw ControlFault("BROWSER_NOT_ACTIVE", "Open a browser on the phone before observing its page.")
        }
        val docId = digestId(packageId + snapshot.nodes.joinToString("|") { it.optString("id") })
        val elements = JSONArray()
        var title = packageId
        var url = ""
        snapshot.nodes.forEach { node ->
            val text = node.optString("label")
            if (node.optBoolean("editable")) browserAddressFromLabel(text)?.let { url = it.take(2048) }
            if (elements.length() < 300 && node.optBoolean("visible", true)) {
                elements.put(JSONObject()
                    .put("tag", node.optString("role"))
                    .put("id", node.optString("resource_id"))
                    .put("role", node.optString("role"))
                    .put("text", text.take(256))
                    .put("label", text.take(256))
                    .put("type", if (node.optBoolean("editable")) "text" else ""))
            }
        }
        val label = runCatching { service.packageManager.getApplicationLabel(service.packageManager.getApplicationInfo(packageId, PackageManager.ApplicationInfoFlags.of(0))).toString() }.getOrDefault(packageId)
        if (title == packageId) title = Uri.parse(url).host?.takeIf { it.isNotBlank() } ?: label
        return JSONObject().put("context_id", packageId).put("document_id", docId).put("title", title).put("url", url).put("elements", elements)
    }

    private fun waitForNode(service: DeviceAccessibilityService, text: String, timeoutMS: Int) {
        val until = System.currentTimeMillis() + timeoutMS
        do {
            if (runCatching { service.snapshot().nodes.any { it.optString("label") == text || it.optString("resource_id") == text } }.getOrDefault(false)) return
            Thread.sleep(150)
        } while (System.currentTimeMillis() < until)
        throw ControlFault("CONDITION_TIMEOUT", "The requested UI element did not appear before the deadline.")
    }

    private fun observation(snapshot: DeviceAccessibilityService.Snapshot): JSONObject {
        return JSONObject().put("frame_id", digestId(Instant.now().toString() + snapshot.nodes.joinToString("|") { it.optString("id") }))
            .put("captured_at", Instant.now().toString())
            .put("width", snapshot.width).put("height", snapshot.height)
            .put("orientation", if (snapshot.width > snapshot.height) "landscape" else "portrait")
            .put("app_id", snapshot.appId).put("nodes", JSONArray(snapshot.nodes))
    }

    private fun success(id: String) = JSONObject().put("id", id).put("state", "succeeded")
    private fun failure(id: String, code: String, message: String) = JSONObject()
        .put("id", id).put("state", "failed")
        .put("error", JSONObject().put("code", code).put("message", message).put("status", 409))
}

internal class ControlFault(val code: String, message: String) : RuntimeException(message)

internal data class TextEdit(val text: String, val cursor: Int)

internal fun insertAtSelection(value: String, selectionStart: Int, selectionEnd: Int, inserted: String): TextEdit {
    val start = selectionStart.takeIf { it in 0..value.length } ?: value.length
    val end = selectionEnd.takeIf { it in start..value.length } ?: start
    return TextEdit(value.substring(0, start) + inserted + value.substring(end), start + inserted.length)
}

internal fun backspaceAtSelection(value: String, selectionStart: Int, selectionEnd: Int): TextEdit {
    val start = selectionStart.takeIf { it in 0..value.length } ?: value.length
    val end = selectionEnd.takeIf { it in start..value.length } ?: start
    if (end > start) return TextEdit(value.removeRange(start, end), start)
    if (start == 0) return TextEdit(value, 0)
    val previousCodePoint = Character.codePointBefore(value, start)
    val deleteStart = start - Character.charCount(previousCodePoint)
    return TextEdit(value.removeRange(deleteStart, start), deleteStart)
}

internal fun browserAddressFromLabel(value: String): String? {
    val raw = value.trim()
    if (raw.isEmpty() || raw.any(Char::isWhitespace)) return null
    val explicitScheme = raw.startsWith("https://", ignoreCase = true) || raw.startsWith("http://", ignoreCase = true)
    if (!explicitScheme && !raw.contains('.') && !raw.equals("localhost", ignoreCase = true)) return null
    val candidate = if (explicitScheme) raw else "https://$raw"
    val parsed = runCatching { URI(candidate) }.getOrNull() ?: return null
    return candidate.takeIf {
        parsed.scheme in setOf("http", "https") && !parsed.host.isNullOrBlank() && parsed.userInfo == null
    }
}

private class GestureTicket(val completed: CountDownLatch) {
    @Volatile var succeeded: Boolean = false
}

private fun JSONObject.point(): Pair<Double, Double> {
    val x = optDouble("x", Double.NaN)
    val y = optDouble("y", Double.NaN)
    if (!x.isFinite() || !y.isFinite() || x < 0 || x >= 1 || y < 0 || y >= 1) throw ControlFault("INVALID_ACTION", "Gesture coordinates must be normalized to the current phone viewport.")
    return x to y
}

private fun digestId(value: String): String = MessageDigest.getInstance("SHA-256")
    .digest(value.toByteArray(Charsets.UTF_8)).take(16).joinToString("") { "%02x".format(it) }

private fun browserDocumentId(snapshot: DeviceAccessibilityService.Snapshot): String =
    digestId(snapshot.appId + snapshot.nodes.joinToString("|") { it.optString("id") })

private fun bounded(value: String, limit: Int): String = value.take(limit)

private fun actionResult(value: Boolean, message: String): Boolean {
    if (!value) throw ControlFault("ACTION_REJECTED", message)
    return true
}
