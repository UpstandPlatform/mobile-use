package org.mobileuse.companion

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import java.net.Inet4Address
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

data class DiscoveredHost(
    val id: String,
    val hostName: String,
    val origin: String,
    val certPin: String,
    val shortFingerprint: String,
)

class LanDiscovery(context: Context) {
    private val nsd = runCatching { context.getSystemService(NsdManager::class.java) }.getOrNull()
    private val executor = Executors.newSingleThreadExecutor()
    private val entries = ConcurrentHashMap<String, DiscoveredHost>()
    private val callbacks = ConcurrentHashMap<String, NsdManager.ServiceInfoCallback>()
    private val _hosts = MutableStateFlow<List<DiscoveredHost>>(emptyList())
    val hosts: StateFlow<List<DiscoveredHost>> = _hosts.asStateFlow()
    // NSD delivers listener callbacks on its own thread, and a stopped listener can report
    // onDiscoveryStopped after a newer listener has started, so only clear our own entry.
    @Volatile private var activeListener: NsdManager.DiscoveryListener? = null

    fun start() {
        val manager = nsd ?: return
        if (activeListener != null) return
        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) {}
            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) { clearListener(this) }
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) { clearListener(this) }
            override fun onDiscoveryStopped(serviceType: String) { clearListener(this) }

            override fun onServiceFound(serviceInfo: NsdServiceInfo) {
                val key = serviceInfo.serviceName
                if (callbacks.containsKey(key)) return
                val cb = object : NsdManager.ServiceInfoCallback {
                    override fun onServiceInfoCallbackRegistrationFailed(errorCode: Int) {
                        callbacks.remove(key, this)
                    }

                    override fun onServiceUpdated(info: NsdServiceInfo) {
                        // Ignore late updates from a callback that stop() or onServiceLost already dropped.
                        if (callbacks[key] !== this) return
                        val ip = info.hostAddresses.firstOrNull { it is Inet4Address }?.hostAddress
                            ?: info.hostAddresses.firstOrNull()?.hostAddress
                            ?: return
                        if (info.port <= 0) return
                        val rawHost = info.attributes["name"]?.toString(Charsets.UTF_8)?.trim().orEmpty()
                        val displayName = rawHost.ifEmpty { info.serviceName }
                        val rawPin = info.attributes["fp"]?.toString(Charsets.UTF_8).orEmpty()
                        val pin = runCatching { normalizeCertPin(rawPin) }.getOrDefault("")
                        val hostPart = if (ip.contains(':')) "[$ip]" else ip
                        val origin = "https://$hostPart:${info.port}"
                        entries[key] = DiscoveredHost(
                            id = key,
                            hostName = displayName,
                            origin = origin,
                            certPin = pin,
                            shortFingerprint = if (pin.isNotEmpty()) formatShortFingerprint(pin) else "",
                        )
                        publish()
                    }

                    override fun onServiceLost() {
                        if (callbacks[key] !== this) return
                        entries.remove(key)
                        publish()
                    }

                    override fun onServiceInfoCallbackUnregistered() {
                        callbacks.remove(key, this)
                    }
                }
                callbacks[key] = cb
                runCatching { manager.registerServiceInfoCallback(serviceInfo, executor, cb) }
                    .onFailure { callbacks.remove(key, cb) }
            }

            override fun onServiceLost(serviceInfo: NsdServiceInfo) {
                val key = serviceInfo.serviceName
                callbacks.remove(key)?.let { cb -> runCatching { manager.unregisterServiceInfoCallback(cb) } }
                entries.remove(key)
                publish()
            }
        }
        activeListener = listener
        runCatching { manager.discoverServices("_mobile-use._tcp.", NsdManager.PROTOCOL_DNS_SD, listener) }
            .onFailure { clearListener(listener) }
    }

    private fun clearListener(listener: NsdManager.DiscoveryListener) {
        if (activeListener === listener) activeListener = null
    }

    fun stop() {
        val manager = nsd ?: return
        activeListener?.let { listener ->
            runCatching { manager.stopServiceDiscovery(listener) }
        }
        activeListener = null
        callbacks.values.forEach { cb -> runCatching { manager.unregisterServiceInfoCallback(cb) } }
        callbacks.clear()
        entries.clear()
        _hosts.value = emptyList()
    }

    private fun publish() {
        _hosts.value = entries.values.sortedBy { it.hostName.lowercase() }
    }
}
