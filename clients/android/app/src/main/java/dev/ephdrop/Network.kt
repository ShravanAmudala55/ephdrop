package dev.ephdrop

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import dev.ephdrop.mobile.Native
import java.net.Inet4Address

/**
 * Tells the Go program this phone's own addresses. Go cannot always list the
 * network interfaces on Android, so the app asks the system instead.
 */
class Network(private val context: Context) : Native {
    override fun localIPs(): String {
        val cm = context.getSystemService(ConnectivityManager::class.java) ?: return ""
        val out = linkedSetOf<String>()
        for (n in cm.allNetworks) {
            val caps = cm.getNetworkCapabilities(n) ?: continue
            if (caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) ||
                caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
            ) continue
            val props = cm.getLinkProperties(n) ?: continue
            for (la in props.linkAddresses) {
                val a = la.address
                if (a is Inet4Address && !a.isLoopbackAddress && !a.isLinkLocalAddress) {
                    a.hostAddress?.let { out.add(it) }
                }
            }
        }
        return out.joinToString(",")
    }
}
