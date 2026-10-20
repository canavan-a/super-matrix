package com.supermatrixapp

import android.app.DownloadManager
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Environment
import androidx.core.app.NotificationCompat
import androidx.core.content.FileProvider
import java.io.File

/**
 * The background half of an app update. A download the user started is
 * remembered here (so it survives the app being closed); when it finishes
 * (UpdateDownloadReceiver) the installer opens at once if Super Matrix is on
 * screen, and otherwise a "ready to install" notification offers it (Android
 * won't let a backgrounded app open the installer itself) and it opens on its
 * own the next time the app comes back (MainActivity.onResume).
 */
object UpdateInstaller {
  private const val PREFS = "supermatrix.update"
  private const val CHANNEL = "supermatrix-updates"
  private const val NOTIFY_ID = 0x5c_0d

  /** Set by MainActivity: whether the app is on screen right now. */
  @Volatile var foreground = false

  private fun prefs(c: Context) = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

  fun apkFile(c: Context, version: String) =
      File(c.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), "supermatrix-$version.apk")

  /** Starts the download in the background; returns at once. */
  fun start(c: Context, url: String, version: String): Long {
    val dm = c.getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager
    prefs(c).getLong("id", -1).takeIf { it >= 0 }?.let { dm.remove(it) } // replace an older one
    val file = apkFile(c, version)
    file.delete() // never let the installer see a half-written file
    val req =
        DownloadManager.Request(Uri.parse(url))
            .setTitle("Super Matrix $version")
            .setDescription("Downloading update")
            .setMimeType("application/vnd.android.package-archive")
            // The system's own notification, with a progress bar, while it runs.
            .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE)
            .setDestinationInExternalFilesDir(c, Environment.DIRECTORY_DOWNLOADS, file.name)
    val id = dm.enqueue(req)
    prefs(c).edit().putLong("id", id).putString("version", version).putBoolean("ready", false).putBoolean("prompted", false).apply()
    return id
  }

  /** The download with this id finished (or failed). */
  fun onDownloadComplete(c: Context, id: Long) {
    val p = prefs(c)
    if (p.getLong("id", -1) != id) return
    val version = p.getString("version", null) ?: return
    val dm = c.getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager
    val ok =
        dm.query(DownloadManager.Query().setFilterById(id))?.use { cur ->
          cur.moveToFirst() &&
              cur.getInt(cur.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS)) == DownloadManager.STATUS_SUCCESSFUL
        } ?: false
    if (!ok || !apkFile(c, version).exists()) {
      p.edit().clear().apply()
      return
    }
    p.edit().putBoolean("ready", true).apply()
    notifyReady(c, version)
    if (foreground) prompt(c)
  }

  /** On returning to the app: open the installer once for a finished download. */
  fun promptIfPending(c: Context) {
    val p = prefs(c)
    if (!p.getBoolean("ready", false) || p.getBoolean("prompted", false)) return
    val version = p.getString("version", null) ?: return
    if (!apkFile(c, version).exists() || !newerThanInstalled(c, version)) {
      p.edit().clear().apply()
      cancelNotice(c)
      return
    }
    prompt(c)
  }

  private fun prompt(c: Context) {
    val version = prefs(c).getString("version", null) ?: return
    try {
      c.startActivity(installIntent(c, version).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
      prefs(c).edit().putBoolean("prompted", true).apply()
    } catch (_: Exception) {
      // The notification still offers it.
    }
  }

  private fun installIntent(c: Context, version: String): Intent {
    val uri = FileProvider.getUriForFile(c, c.packageName + ".updateprovider", apkFile(c, version))
    return Intent(Intent.ACTION_VIEW)
        .setDataAndType(uri, "application/vnd.android.package-archive")
        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
  }

  private fun notifyReady(c: Context, version: String) {
    val nm = c.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && nm.getNotificationChannel(CHANNEL) == null) {
      nm.createNotificationChannel(NotificationChannel(CHANNEL, "App updates", NotificationManager.IMPORTANCE_HIGH))
    }
    val tap =
        PendingIntent.getActivity(
            c,
            NOTIFY_ID,
            installIntent(c, version),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    val n =
        NotificationCompat.Builder(c, CHANNEL)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("Super Matrix $version is ready")
            .setContentText("Tap to install the update.")
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setContentIntent(tap)
            .setAutoCancel(true)
            .build()
    try {
      nm.notify(NOTIFY_ID, n)
    } catch (_: SecurityException) {
      // Notifications are off; it'll still open when the app comes back.
    }
  }

  private fun cancelNotice(c: Context) {
    (c.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).cancel(NOTIFY_ID)
  }

  private fun newerThanInstalled(c: Context, version: String): Boolean {
    val installed = c.packageManager.getPackageInfo(c.packageName, 0).versionName ?: return true
    val a = version.split('.', '-').map { it.toIntOrNull() ?: 0 }
    val b = installed.split('.', '-').map { it.toIntOrNull() ?: 0 }
    for (i in 0 until maxOf(a.size, b.size)) {
      val d = a.getOrElse(i) { 0 } - b.getOrElse(i) { 0 }
      if (d != 0) return d > 0
    }
    return false
  }
}
