package org.mobileuse.companion

import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
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
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

internal class ControlSessionService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var sessionId: String? = null
    private var pairing: Pairing? = null
    private var stream: ControlCommandStream? = null
    private var heartbeatJob: Job? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            endSession()
            return START_NOT_STICKY
        }
        val requestedSession = intent?.getStringExtra(EXTRA_SESSION) ?: ControlSessionSettings.session(this)
        val currentPairing = PairingStore(this).read()
        startForeground(
            CompanionNotifications.CONTROL_NOTIFICATION_ID,
            notification(),
            ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE,
        )
        if (requestedSession.isNullOrBlank() || currentPairing == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (sessionId == requestedSession && stream != null) return START_STICKY
        closeSession()
        sessionId = requestedSession
        pairing = currentPairing
        ControlSessionSettings.set(this, requestedSession)
        stream = ControlCommandStream(this, currentPairing, requestedSession) {
            ControlSessionSettings.set(this, null)
            CompanionNotifications.postAlert(
                this,
                "control-session-$requestedSession",
                "Phone access session ended",
                "The approved session is no longer active. Review any new request on your phone.",
            )
            stopSelf()
        }.also { it.start() }
        heartbeatJob = scope.launch { maintainSession(currentPairing, requestedSession) }
        return START_STICKY
    }

    private suspend fun maintainSession(currentPairing: Pairing, id: String) {
        var retryMs = 1500L
        while (scope.isActive && sessionId == id) {
            try {
                val heartbeat = CompanionApi(currentPairing).heartbeat(id, PhoneControlCapabilities.current(this))
                CompanionNotifications.syncRequests(this, heartbeat)
                val active = heartbeat.sessions.any { it.id == id && it.state == "active" && !it.paused }
                if (!active) {
                    ControlSessionSettings.set(this, null)
                    stopSelf()
                    return
                }
                retryMs = 4000L
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: ApiFailure) {
                if (error.status in setOf(401, 403, 404, 409)) {
                    ControlSessionSettings.set(this, null)
                    stopSelf()
                    return
                }
                retryMs = (retryMs * 2).coerceAtMost(15_000L)
                CompanionNotifications.connectionChanged(this, false, error.message)
            } catch (_: Exception) {
                retryMs = (retryMs * 2).coerceAtMost(15_000L)
                CompanionNotifications.connectionChanged(this, false)
            }
            delay(retryMs)
        }
    }

    private fun endSession() {
        val currentPairing = pairing ?: PairingStore(this).read()
        val currentSession = sessionId ?: ControlSessionSettings.session(this)
        scope.launch {
            if (currentPairing != null && !currentSession.isNullOrBlank()) {
                runCatching { CompanionApi(currentPairing).session(currentSession, "end") }
            }
            ControlSessionSettings.set(this@ControlSessionService, null)
            stopSelf()
        }
    }

    private fun notification() = CompanionNotifications.controlNotification(
        this,
        PendingIntent.getService(
            this,
            204,
            Intent(this, ControlSessionService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        ),
    )

    private fun closeSession() {
        heartbeatJob?.cancel()
        heartbeatJob = null
        stream?.close()
        stream = null
    }

    override fun onDestroy() {
        closeSession()
        scope.cancel()
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    companion object {
        private const val ACTION_STOP = "org.mobileuse.companion.CONTROL_STOP"
        private const val EXTRA_SESSION = "control.session"

        fun start(context: Context, sessionId: String) {
            ContextCompat.startForegroundService(
                context,
                Intent(context, ControlSessionService::class.java).putExtra(EXTRA_SESSION, sessionId),
            )
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, ControlSessionService::class.java))
            ControlSessionSettings.set(context, null)
        }
    }
}

private object ControlSessionSettings {
    private const val PREFS = "control_session"
    private const val SESSION = "active_session"

    fun session(context: Context): String? = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(SESSION, null)
    fun set(context: Context, value: String?) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(SESSION, value).apply()
    }
}
