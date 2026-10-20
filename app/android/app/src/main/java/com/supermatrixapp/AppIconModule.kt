package com.supermatrixapp

import android.content.ComponentName
import android.content.pm.PackageManager
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Switches the launcher icon between the per-theme versions.
 *
 * Android has no API to change an app's icon directly. The manifest instead
 * declares one launcher entry per theme (an <activity-alias> named IconLight,
 * IconDark, ..., each with its own icon) and exactly one of them is enabled at
 * any time; enabling another swaps the icon the launcher shows. The JS side
 * (src/appIcon.ts) only calls this while the app is going to the background,
 * because some launchers flicker, drop a pinned shortcut, or restart the app
 * when the entry they point at is disabled.
 */
class AppIconModule(private val reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

  override fun getName(): String = "AppIcon"

  private val themes = listOf("light", "dark", "slate", "sepia", "ember")

  private fun alias(theme: String) =
      ComponentName(
          reactContext.packageName,
          "com.supermatrixapp.Icon" + theme.replaceFirstChar { it.uppercase() })

  /** The manifest enables IconLight by default; the others start disabled. */
  private fun isEnabled(theme: String): Boolean =
      when (reactContext.packageManager.getComponentEnabledSetting(alias(theme))) {
        PackageManager.COMPONENT_ENABLED_STATE_ENABLED -> true
        PackageManager.COMPONENT_ENABLED_STATE_DEFAULT -> theme == DEFAULT
        else -> false
      }

  /** Resolves with the theme whose icon is currently showing. */
  @ReactMethod
  fun getIcon(promise: Promise) {
    promise.resolve(themes.firstOrNull { isEnabled(it) } ?: DEFAULT)
  }

  /** Makes [name]'s icon the launcher icon. Resolves true on success. */
  @ReactMethod
  fun setIcon(name: String, promise: Promise) {
    if (name !in themes) {
      promise.reject("E_BAD_ICON", "Unknown icon: $name")
      return
    }
    try {
      val pm = reactContext.packageManager
      // Enable the new entry before disabling the old ones, so there is never
      // a moment with no launcher entry at all. DONT_KILL_APP keeps the app
      // running while its own launcher entry changes.
      pm.setComponentEnabledSetting(
          alias(name), PackageManager.COMPONENT_ENABLED_STATE_ENABLED, PackageManager.DONT_KILL_APP)
      for (t in themes) {
        if (t != name) {
          pm.setComponentEnabledSetting(
              alias(t), PackageManager.COMPONENT_ENABLED_STATE_DISABLED, PackageManager.DONT_KILL_APP)
        }
      }
      promise.resolve(true)
    } catch (e: Exception) {
      promise.reject("E_SET_ICON", e)
    }
  }

  companion object {
    private const val DEFAULT = "dark"
  }
}
