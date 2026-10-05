package dev.ephdrop

import android.Manifest
import android.annotation.SuppressLint
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import com.google.android.gms.code.scanner.GmsBarcodeScannerOptions
import com.google.android.gms.code.scanner.GmsBarcodeScanning
import com.google.android.gms.mlkit.barcode.common.Barcode
import dev.ephdrop.mobile.Agent
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/** The window: the same page as the desktop app, shown in a web view. */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView
    private var chooser: ValueCallback<Array<Uri>>? = null

    private val pick = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { res ->
        val cb = chooser
        chooser = null
        cb?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(res.resultCode, res.data))
    }
    private val askNotifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    private val closed = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) = finishAndRemoveTask()
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        web = WebView(this)
        setContentView(web)
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }
        web.webViewClient = object : WebViewClient() {
            // The window only ever shows the page served by our own program.
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean =
                request.url.host != "127.0.0.1"
        }
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                view: WebView,
                callback: ValueCallback<Array<Uri>>,
                params: FileChooserParams,
            ): Boolean {
                chooser?.onReceiveValue(null)
                chooser = callback
                return try {
                    pick.launch(params.createIntent())
                    true
                } catch (_: Exception) {
                    chooser = null
                    false
                }
            }
        }
        web.addJavascriptInterface(Bridge(), "ephdropPhone")

        ContextCompat.registerReceiver(
            this, closed, IntentFilter(EphdropService.ACTION_CLOSED), ContextCompat.RECEIVER_NOT_EXPORTED,
        )

        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) askNotifications.launch(Manifest.permission.POST_NOTIFICATIONS)

        ContextCompat.startForegroundService(this, Intent(this, EphdropService::class.java))
        Engine.whenReady { agent -> web.loadUrl(agent.pageURL()) }
        handleShare(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleShare(intent)
    }

    override fun onDestroy() {
        unregisterReceiver(closed)
        chooser?.onReceiveValue(null)
        web.destroy()
        super.onDestroy()
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        // One page only: back leaves the app but keeps sharing in the background.
        moveTaskToBack(true)
    }

    // ---------------------------------------------------- share sheet

    private fun handleShare(intent: Intent?) {
        if (intent == null) return
        val uris: List<Uri> = when (intent.action) {
            Intent.ACTION_SEND -> listOfNotNull(streamOf(intent))
            Intent.ACTION_SEND_MULTIPLE -> streamsOf(intent)
            else -> return
        }
        if (uris.isEmpty()) return
        intent.action = Intent.ACTION_MAIN // do not share again if the window is recreated
        Toast.makeText(this, "Sharing with your devices...", Toast.LENGTH_SHORT).show()
        Engine.whenReady { agent ->
            Thread {
                var ok = 0
                for (u in uris) if (upload(agent, u)) ok++
                runOnUiThread {
                    val text = when {
                        ok == uris.size && ok == 1 -> "Shared 1 file"
                        ok == uris.size -> "Shared $ok files"
                        else -> "Could not share ${uris.size - ok} of ${uris.size} files"
                    }
                    Toast.makeText(this, text, Toast.LENGTH_LONG).show()
                }
            }.start()
        }
    }

    @Suppress("DEPRECATION")
    private fun streamOf(i: Intent): Uri? =
        if (Build.VERSION.SDK_INT >= 33) i.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
        else i.getParcelableExtra(Intent.EXTRA_STREAM)

    @Suppress("DEPRECATION")
    private fun streamsOf(i: Intent): List<Uri> =
        (if (Build.VERSION.SDK_INT >= 33) i.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java)
        else i.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM)) ?: emptyList()

    private fun displayName(u: Uri): String {
        contentResolver.query(u, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0)?.takeIf { it.isNotBlank() }?.let { return it }
        }
        return u.lastPathSegment?.substringAfterLast('/') ?: "file"
    }

    /** Streams a shared file to our own program, one chunk at a time. */
    private fun upload(agent: Agent, u: Uri): Boolean = try {
        val url = URL("http://127.0.0.1:${agent.port()}/api/upload?name=${Uri.encode(displayName(u))}&ttlHours=24")
        val c = url.openConnection() as HttpURLConnection
        try {
            c.requestMethod = "PUT"
            c.doOutput = true
            c.setChunkedStreamingMode(64 * 1024)
            c.setRequestProperty("Authorization", "Bearer " + agent.token())
            c.setRequestProperty("Content-Type", "application/octet-stream")
            contentResolver.openInputStream(u)!!.use { input -> c.outputStream.use { input.copyTo(it) } }
            c.responseCode == 200
        } finally {
            c.disconnect()
        }
    } catch (_: Exception) {
        false
    }

    // ---------------------------------------------------- what the page may ask for

    inner class Bridge {
        /** Opens the QR scanner and hands the result to the page. */
        @JavascriptInterface
        fun scanInvite() {
            runOnUiThread {
                val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).build()
                GmsBarcodeScanning.getClient(this@MainActivity, options).startScan()
                    .addOnSuccessListener { b ->
                        val text = b.rawValue ?: return@addOnSuccessListener
                        web.evaluateJavascript("window.onInviteScanned(" + JSONObject.quote(text) + ")", null)
                    }
                    .addOnFailureListener {
                        Toast.makeText(this@MainActivity, "Could not open the scanner. Paste the code instead.", Toast.LENGTH_LONG).show()
                    }
            }
        }

        /** Moves a file the program just fetched into Downloads. */
        @JavascriptInterface
        fun exportFile(path: String, name: String) {
            val app = applicationContext
            Thread { Exporter.toDownloads(app, path, name) }.start()
        }
    }
}
