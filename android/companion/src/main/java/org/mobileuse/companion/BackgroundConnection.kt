package org.mobileuse.companion

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.IBinder
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

internal object BackgroundConnectionSettings {
    private const val PREFS = "background_connection"
    private const val ENABLED = "enabled"
    val state = MutableStateFlow(false)

    fun enabled(context: Context): Boolean =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(ENABLED, false)

    fun setEnabled(context: Context, enabled: Boolean) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean(ENABLED, enabled).apply()
        state.value = enabled
    }

    fun refresh(context: Context) { state.value = enabled(context) }
}

internal object BackgroundConnectionRuntime {
    @Volatile var activityVisible: Boolean = false
}

internal object CompanionNotifications {
    private const val PREFS = "companion_notifications"
    private const val PREF_SESSIONS = "pending_sessions"
    private const val PREF_SENSORS = "pending_sensors"
    private const val PREF_CONNECTION = "last_connection"
    private const val PREF_SCREEN_ERROR = "last_screen_error"
    const val MONITOR_CHANNEL = "background-connection"
    private const val REQUEST_CHANNEL = "access-requests"
    private const val ALERT_CHANNEL = "connection-alerts"
    const val MONITOR_NOTIFICATION_ID = 201
    private const val SCREEN_ERROR_NOTIFICATION_ID = 203
    private val lock = Any()

    fun notificationsAllowed(context: Context): Boolean {
        if (context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return false
        val manager = context.getSystemService(NotificationManager::class.java)
        return manager.areNotificationsEnabled()
    }

    fun createChannels(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(MONITOR_CHANNEL, "Background connection", NotificationManager.IMPORTANCE_LOW).apply {
                description = "Shows when mobile-use is keeping session requests available in the background."
                lockscreenVisibility = Notification.VISIBILITY_PRIVATE
            },
        )
        manager.createNotificationChannel(
            NotificationChannel(REQUEST_CHANNEL, "Approvals and sensor requests", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "Alerts you when a computer requests a session or an approved phone sensor."
                lockscreenVisibility = Notification.VISIBILITY_PRIVATE
            },
        )
        manager.createNotificationChannel(
            NotificationChannel(ALERT_CHANNEL, "Connection and sharing alerts", NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = "Reports connection changes and screen-sharing problems."
                lockscreenVisibility = Notification.VISIBILITY_PRIVATE
            },
        )
    }

    fun monitorNotification(context: Context, text: String): Notification {
        createChannels(context)
        val open = openIntent(context, "background")
        val stop = PendingIntent.getService(
            context,
            202,
            Intent(context, BackgroundMonitorService::class.java).setAction(BackgroundMonitorService.ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(context, MONITOR_CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("mobile-use background connection")
            .setContentText(text)
            .setContentIntent(open)
            .setPublicVersion(publicNotification(context, MONITOR_CHANNEL, "mobile-use is connected", "Tap to open the companion app."))
            .setCategory(Notification.CATEGORY_SERVICE)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .addAction(Notification.Action.Builder(null, "Turn off", stop).build())
            .build()
    }

    fun syncRequests(context: Context, heartbeat: Heartbeat) {
        createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        synchronized(lock) {
            val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            val showNotifications = !BackgroundConnectionRuntime.activityVisible
            val sessions = heartbeat.sessions.filter { it.state == "pending_approval" }
            val sessionIds = sessions.map { it.id }.toSet()
            val previousSessionIds = prefs.getStringSet(PREF_SESSIONS, emptySet()).orEmpty()
            (previousSessionIds - sessionIds).forEach { manager.cancel(notificationId("session", it)) }
            sessions.forEach { session ->
                if (!showNotifications) return@forEach
                val privateText = session.purpose.take(100).ifBlank { "Review this access request on your phone." }
                manager.notify(
                    notificationId("session", session.id),
                    requestNotification(context, "Session approval requested", privateText, session.id),
                )
            }

            val sensorIds = heartbeat.sensors.map { it.id }.toSet()
            val previousSensorIds = prefs.getStringSet(PREF_SENSORS, emptySet()).orEmpty()
            (previousSensorIds - sensorIds).forEach { manager.cancel(notificationId("sensor", it)) }
            heartbeat.sensors.forEach { request ->
                if (!showNotifications) return@forEach
                val text = if (request.type == "camera") "A computer requested a camera photo."
                else "A computer requested a ${request.seconds}-second microphone clip."
                manager.notify(
                    notificationId("sensor", request.id),
                    requestNotification(context, "Sensor request needs your review", text, request.id),
                )
            }
            prefs.edit()
                .putStringSet(PREF_SESSIONS, sessionIds)
                .putStringSet(PREF_SENSORS, sensorIds)
                .apply()
        }
    }

    fun connectionChanged(context: Context, online: Boolean, detail: String? = null) {
        createChannels(context)
        if (BackgroundConnectionRuntime.activityVisible) return
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        synchronized(lock) {
            val previous = prefs.getInt(PREF_CONNECTION, -1)
            prefs.edit().putInt(PREF_CONNECTION, if (online) 1 else 0).apply()
            if (previous == -1 || previous == if (online) 1 else 0) return
        }
        if (online) {
            postAlert(context, "connection", "mobile-use is back online", "Session requests can reach this phone again.")
        } else {
            postAlert(
                context,
                "connection",
                "mobile-use connection interrupted",
                detail ?: "The companion is reconnecting. Check the network if this continues.",
            )
        }
    }

    fun screenShareError(context: Context, message: String?) {
        if (message.isNullOrBlank()) return
        if (BackgroundConnectionRuntime.activityVisible) return
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        synchronized(lock) {
            if (prefs.getString(PREF_SCREEN_ERROR, null) == message) return
            prefs.edit().putString(PREF_SCREEN_ERROR, message).apply()
        }
        context.getSystemService(NotificationManager::class.java).notify(
            SCREEN_ERROR_NOTIFICATION_ID,
            alertNotification(context, "Screen sharing needs attention", message, "screen-sharing"),
        )
    }

    fun clearScreenShareError(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().remove(PREF_SCREEN_ERROR).apply()
        context.getSystemService(NotificationManager::class.java).cancel(SCREEN_ERROR_NOTIFICATION_ID)
    }

    fun clearPending(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java)
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        synchronized(lock) {
            prefs.getStringSet(PREF_SESSIONS, emptySet()).orEmpty().forEach { manager.cancel(notificationId("session", it)) }
            prefs.getStringSet(PREF_SENSORS, emptySet()).orEmpty().forEach { manager.cancel(notificationId("sensor", it)) }
            prefs.edit().remove(PREF_SESSIONS).remove(PREF_SENSORS).apply()
        }
    }

    fun postAlert(context: Context, key: String, title: String, text: String) {
        createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        val id = notificationId("alert", key)
        manager.notify(id, alertNotification(context, title, text, key))
    }

    private fun requestNotification(context: Context, title: String, text: String, key: String): Notification =
        Notification.Builder(context, REQUEST_CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(openIntent(context, key))
            .setPublicVersion(publicNotification(context, REQUEST_CHANNEL, "mobile-use needs your review", "Unlock your phone to review this request."))
            .setCategory(Notification.CATEGORY_EVENT)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .addAction(Notification.Action.Builder(null, "Review", openIntent(context, key)).build())
            .build()

    private fun alertNotification(context: Context, title: String, text: String, key: String): Notification =
        Notification.Builder(context, ALERT_CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text.take(180))
            .setContentIntent(openIntent(context, key))
            .setPublicVersion(publicNotification(context, ALERT_CHANNEL, "mobile-use needs attention", "Unlock your phone to view the details."))
            .setCategory(Notification.CATEGORY_STATUS)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)
            .build()

    private fun publicNotification(context: Context, channel: String, title: String, text: String): Notification =
        Notification.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .build()

    private fun openIntent(context: Context, key: String): PendingIntent {
        val intent = Intent(context, MainActivity::class.java)
            .setAction("${context.packageName}.OPEN.$key")
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        return PendingIntent.getActivity(
            context,
            notificationId("open", key),
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun notificationId(type: String, value: String): Int =
        ("mobile-use:$type:$value").hashCode().and(Int.MAX_VALUE).coerceAtLeast(1)
}

class BackgroundMonitorService : Service() {
    private val serviceScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var monitorJob: Job? = null
    @Volatile private var connectionText = "Connecting to the paired computer…"

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            setEnabled(this, false)
            CompanionNotifications.clearPending(this)
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return START_NOT_STICKY
        }
        if (!BackgroundConnectionSettings.enabled(this) || PairingStore(this).read() == null ||
            !CompanionNotifications.notificationsAllowed(this)
        ) {
            BackgroundConnectionSettings.setEnabled(this, false)
            stopSelf()
            return START_NOT_STICKY
        }
        runCatching {
            startForeground(
                CompanionNotifications.MONITOR_NOTIFICATION_ID,
                CompanionNotifications.monitorNotification(this, connectionText),
                ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE,
            )
        }.onFailure {
            BackgroundConnectionSettings.setEnabled(this, false)
            stopSelf()
            return START_NOT_STICKY
        }
        if (monitorJob?.isActive != true) monitorJob = serviceScope.launch { monitor() }
        return START_STICKY
    }

    private suspend fun monitor() {
        var retryMs = 5_000L
        var currentPairing: Pairing? = null
        var api: CompanionApi? = null
        while (serviceScope.isActive) {
            if (!BackgroundConnectionSettings.enabled(this)) {
                stopSelf()
                return
            }
            if (BackgroundConnectionRuntime.activityVisible || Sharing.session.value != null) {
                delay(1_000L)
                continue
            }
            val pairing = PairingStore(this).read()
            if (pairing == null) {
                setEnabled(this, false)
                CompanionNotifications.clearPending(this)
                stopSelf()
                return
            }
            if (pairing != currentPairing) {
                currentPairing = pairing
                api = CompanionApi(pairing)
            }
            try {
                val heartbeat = api!!.heartbeat(null)
                CompanionNotifications.syncRequests(this, heartbeat)
                CompanionNotifications.connectionChanged(this, true)
                connectionText = "Connected · session requests and alerts are on."
                getSystemService(NotificationManager::class.java).notify(
                    CompanionNotifications.MONITOR_NOTIFICATION_ID,
                    CompanionNotifications.monitorNotification(this, connectionText),
                )
                retryMs = 5_000L
                delay(retryMs)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                val detail = (error as? ApiFailure)?.message
                CompanionNotifications.connectionChanged(this, false, detail)
                connectionText = "Disconnected · reconnecting in the background."
                getSystemService(NotificationManager::class.java).notify(
                    CompanionNotifications.MONITOR_NOTIFICATION_ID,
                    CompanionNotifications.monitorNotification(this, connectionText),
                )
                delay(retryMs)
                retryMs = (retryMs * 2).coerceAtMost(30_000L)
            }
        }
    }

    override fun onDestroy() {
        monitorJob?.cancel()
        serviceScope.cancel()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        const val ACTION_STOP = "org.mobileuse.companion.BACKGROUND_STOP"
        private const val ACTION_START = "org.mobileuse.companion.BACKGROUND_START"

        fun setEnabled(context: Context, enabled: Boolean): Boolean {
            if (!enabled) {
                BackgroundConnectionSettings.setEnabled(context, false)
                context.stopService(Intent(context, BackgroundMonitorService::class.java))
                CompanionNotifications.clearPending(context)
                return true
            }
            if (PairingStore(context).read() == null || !CompanionNotifications.notificationsAllowed(context)) {
                BackgroundConnectionSettings.setEnabled(context, false)
                return false
            }
            BackgroundConnectionSettings.setEnabled(context, true)
            return runCatching {
                ContextCompat.startForegroundService(
                    context,
                    Intent(context, BackgroundMonitorService::class.java).setAction(ACTION_START),
                )
                true
            }.getOrElse {
                BackgroundConnectionSettings.setEnabled(context, false)
                false
            }
        }

        fun startIfEnabled(context: Context): Boolean =
            !BackgroundConnectionSettings.enabled(context) || setEnabled(context, true)
    }
}

class BackgroundMonitorBootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED ||
            !BackgroundConnectionSettings.enabled(context) || PairingStore(context).read() == null ||
            !CompanionNotifications.notificationsAllowed(context)
        ) return
        runCatching {
            ContextCompat.startForegroundService(
                context,
                Intent(context, BackgroundMonitorService::class.java),
            )
        }.onFailure {
            CompanionNotifications.postAlert(
                context,
                "boot-reconnect",
                "Open mobile-use to reconnect",
                "Android could not restart the background connection after reboot.",
            )
        }
    }
}
