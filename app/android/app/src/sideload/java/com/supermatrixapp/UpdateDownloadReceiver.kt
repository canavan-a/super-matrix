package com.supermatrixapp

import android.app.DownloadManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Declared in the manifest, so an update download finishing reaches
 * UpdateInstaller even when Super Matrix isn't running.
 */
class UpdateDownloadReceiver : BroadcastReceiver() {
  override fun onReceive(context: Context, intent: Intent) {
    if (intent.action != DownloadManager.ACTION_DOWNLOAD_COMPLETE) return
    val id = intent.getLongExtra(DownloadManager.EXTRA_DOWNLOAD_ID, -1)
    if (id >= 0) UpdateInstaller.onDownloadComplete(context.applicationContext, id)
  }
}
