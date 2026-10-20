package com.supermatrixapp

import android.app.Activity
import com.facebook.react.ReactPackage

/**
 * What only the sideloaded build (distributed outside Google Play, see
 * release/release.sh) has: the in-app updater. The play flavor's Flavor.kt
 * has none of it, so the Play build never contains self-update code.
 */
object Flavor {
  fun packages(): List<ReactPackage> = listOf(AppUpdaterPackage())

  // Lets a finished update download open the installer straight away while
  // the app is on screen, or as soon as it comes back (UpdateInstaller.kt).
  fun onResume(activity: Activity) {
    UpdateInstaller.foreground = true
    UpdateInstaller.promptIfPending(activity)
  }

  fun onPause() {
    UpdateInstaller.foreground = false
  }
}
