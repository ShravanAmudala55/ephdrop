package dev.ephdrop

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Environment
import android.os.IBinder
import android.provider.Settings
import androidx.core.app.ServiceCompat
import dev.ephdrop.mobile.Mobile
import java.io.File

/**
 * Keeps ephdrop running while the app is in the background, so other devices
 * can fetch files from this phone. Android requires the notification.
 */
class EphdropService : Service() {
    private var discovery: Discovery? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        val channel = NotificationChannel(CHANNEL, "Sharing", NotificationManager.IMPORTANCE_LOW)
        channel.description = "Shown while ephdrop is available to your other devices"
        getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
            // close the window too, if it is open
            sendBroadcast(Intent(ACTION_CLOSED).setPackage(packageName))
            return START_NOT_STICKY
        }
        try {
            ServiceCompat.startForeground(
                this, NOTIFICATION_ID, notification(),
                ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE,
            )
        } catch (e: Exception) {
            // Android refused to start it from the background. It starts again when the app is opened.
            stopSelf()
            return START_NOT_STICKY
        }
        if (Engine.agent == null) startProgram()
        return START_STICKY
    }

    private fun startProgram() {
        Thread {
            try {
                val data = File(filesDir, "ephdrop").apply { mkdirs() }
                val agent = Mobile.start(data.absolutePath, deviceName(), downloadsDir(this).absolutePath, Network(this))
                Engine.set(agent)
                discovery = Discovery(this, agent).also { it.start() }
            } catch (e: Exception) {
                Engine.fail(e.message ?: e.toString())
            }
        }.start()
    }

    override fun onDestroy() {
        discovery?.stop()
        discovery = null
        val a = Engine.agent
        Engine.clear()
        if (a != null) Thread { try { a.stop() } catch (_: Exception) {} }.start()
        super.onDestroy()
    }

    private fun notification(): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, EphdropService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle("ephdrop is on")
            .setContentText("Your other devices can see files shared from this phone")
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(0, "Stop sharing", stop)
            .build()
    }

    private fun deviceName(): String {
        val n = try {
            Settings.Global.getString(contentResolver, "device_name")
        } catch (_: Exception) {
            null
        }
        return n?.takeIf { it.isNotBlank() } ?: Build.MODEL
    }

    companion object {
        const val CHANNEL = "sharing"
        const val NOTIFICATION_ID = 1
        const val ACTION_STOP = "dev.ephdrop.STOP"
        const val ACTION_CLOSED = "dev.ephdrop.CLOSED"

        /** Where files fetched from other devices land first. */
        fun downloadsDir(c: android.content.Context): File =
            (c.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: File(c.filesDir, "downloads")).apply { mkdirs() }
    }
}
