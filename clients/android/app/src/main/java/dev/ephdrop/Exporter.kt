package dev.ephdrop

import android.content.ContentValues
import android.content.Context
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import android.widget.Toast
import java.io.File

/** Moves a file fetched from another device into the phone's Downloads folder. */
object Exporter {
    fun toDownloads(context: Context, path: String, name: String) {
        val main = Handler(Looper.getMainLooper())
        fun say(text: String) = main.post { Toast.makeText(context, text, Toast.LENGTH_LONG).show() }
        try {
            val src = File(path).canonicalFile
            val root = EphdropService.downloadsDir(context).canonicalFile
            // Only files the program itself fetched, never any other file in the app.
            require(src.path.startsWith(root.path + File.separator) && src.isFile) { "not a fetched file" }
            val clean = name.replace(Regex("[\\\\/:*?\"<>|\\u0000-\\u001f]"), "_").trim().ifEmpty { "file" }
            val ext = clean.substringAfterLast('.', "").lowercase()
            val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, clean)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.RELATIVE_PATH, "Download/ephdrop")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val resolver = context.contentResolver
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: error("could not create the file")
            try {
                resolver.openOutputStream(uri)!!.use { out -> src.inputStream().use { it.copyTo(out) } }
                values.clear()
                values.put(MediaStore.Downloads.IS_PENDING, 0)
                resolver.update(uri, values, null, null)
            } catch (e: Exception) {
                resolver.delete(uri, null, null)
                throw e
            }
            src.delete()
            say("Saved $clean to Downloads/ephdrop")
        } catch (e: Exception) {
            say("Could not save the file: ${e.message}")
        }
    }
}
