package com.supermatrixapp

import android.os.Bundle
import android.view.WindowManager
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate

class MainActivity : ReactActivity() {

  override fun onCreate(savedInstanceState: Bundle?) {
    // Always on, set before anything is drawn: blocks screenshots and screen
    // recording, and blanks the app's thumbnail in the recent-apps switcher.
    // (Off only in a build made with -PallowScreenshots=true, for store screenshots.)
    if (!BuildConfig.ALLOW_SCREENSHOTS) {
      window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
    }
    super.onCreate(savedInstanceState)
  }

  // Build-specific hooks (the sideload build's updater; see Flavor.kt).
  override fun onResume() {
    super.onResume()
    Flavor.onResume(this)
  }

  override fun onPause() {
    Flavor.onPause()
    super.onPause()
  }

  /**
   * Returns the name of the main component registered from JavaScript. This is used to schedule
   * rendering of the component.
   */
  override fun getMainComponentName(): String = "SupermatrixApp"

  /**
   * Returns the instance of the [ReactActivityDelegate]. We use [DefaultReactActivityDelegate]
   * which allows you to enable New Architecture with a single boolean flags [fabricEnabled]
   */
  override fun createReactActivityDelegate(): ReactActivityDelegate =
      DefaultReactActivityDelegate(this, mainComponentName, fabricEnabled)
}
