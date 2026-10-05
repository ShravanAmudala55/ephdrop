package dev.ephdrop

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.Handler
import android.os.Looper
import dev.ephdrop.mobile.Agent
import java.net.Inet4Address

/**
 * Finds other devices and announces this one, using Android's own network
 * service discovery. What it hears is passed to the Go program with
 * Agent.seen. The Go program forgets a device that is not heard for two
 * minutes, so everything known is reported again every 30 seconds.
 */
class Discovery(context: Context, private val agent: Agent) {
    private val nsd = context.getSystemService(NsdManager::class.java)
    private val main = Handler(Looper.getMainLooper())

    private class Known(val id: String, val ips: String, val port: Int, val version: String)

    private val known = HashMap<String, Known>() // by service name; main thread only
    private val queue = ArrayDeque<NsdServiceInfo>()
    private var resolving = false
    private var running = false

    private val registration = object : NsdManager.RegistrationListener {
        override fun onServiceRegistered(info: NsdServiceInfo) {}
        override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {}
        override fun onServiceUnregistered(info: NsdServiceInfo) {}
        override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
    }

    private val browser = object : NsdManager.DiscoveryListener {
        override fun onDiscoveryStarted(type: String) {}
        override fun onDiscoveryStopped(type: String) {}
        override fun onStartDiscoveryFailed(type: String, code: Int) {}
        override fun onStopDiscoveryFailed(type: String, code: Int) {}
        override fun onServiceFound(info: NsdServiceInfo) {
            main.post { if (running) enqueue(info) }
        }

        override fun onServiceLost(info: NsdServiceInfo) {
            main.post {
                known.remove(info.serviceName)?.let { agent.gone(it.id) }
            }
        }
    }

    private val refresh = object : Runnable {
        override fun run() {
            if (!running) return
            for (k in known.values) report(k)
            main.postDelayed(this, 30_000)
        }
    }

    fun start() {
        main.post {
            running = true
            val id = agent.deviceId()
            val me = NsdServiceInfo().apply {
                serviceName = "ephdrop-" + id.take(12)
                serviceType = SERVICE_TYPE
                port = agent.transferPort().toInt()
                setAttribute("v", "1")
                setAttribute("id", id)
            }
            try {
                nsd.registerService(me, NsdManager.PROTOCOL_DNS_SD, registration)
                nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, browser)
            } catch (_: Exception) {
                // Discovery failing leaves a manual address as the way to connect.
            }
            main.postDelayed(refresh, 30_000)
        }
    }

    fun stop() {
        main.post {
            running = false
            main.removeCallbacks(refresh)
            try { nsd.unregisterService(registration) } catch (_: Exception) {}
            try { nsd.stopServiceDiscovery(browser) } catch (_: Exception) {}
            known.clear()
            queue.clear()
        }
    }

    // Android resolves one service at a time.
    private fun enqueue(info: NsdServiceInfo) {
        queue.addLast(info)
        next()
    }

    private fun next() {
        if (resolving) return
        val info = queue.removeFirstOrNull() ?: return
        resolving = true
        try {
            nsd.resolveService(info, object : NsdManager.ResolveListener {
                override fun onResolveFailed(i: NsdServiceInfo, code: Int) {
                    main.post { resolving = false; next() }
                }

                override fun onServiceResolved(i: NsdServiceInfo) {
                    main.post {
                        resolving = false
                        if (running) handle(i)
                        next()
                    }
                }
            })
        } catch (_: Exception) {
            resolving = false
        }
    }

    @Suppress("DEPRECATION")
    private fun handle(i: NsdServiceInfo) {
        val attrs = i.attributes
        val id = attrs["id"]?.let { String(it) } ?: return
        val version = attrs["v"]?.let { String(it) } ?: return
        val ips = if (Build.VERSION.SDK_INT >= 34) {
            i.hostAddresses.filterIsInstance<Inet4Address>().mapNotNull { it.hostAddress }
        } else {
            listOfNotNull((i.host as? Inet4Address)?.hostAddress)
        }
        if (ips.isEmpty()) return
        val k = Known(id, ips.joinToString(","), i.port, version)
        known[i.serviceName] = k
        report(k)
    }

    private fun report(k: Known) {
        try {
            agent.seen(k.id, k.ips, k.port.toLong(), k.version)
        } catch (_: Exception) {
            // not a valid ephdrop announcement; ignore it
        }
    }

    companion object {
        const val SERVICE_TYPE = "_ephdrop._tcp"
    }
}
