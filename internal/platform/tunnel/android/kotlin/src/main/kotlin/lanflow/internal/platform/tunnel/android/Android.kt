package lanflow.internal.platform.tunnel.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.os.Build
import dev.mygo.Native
import dev.mygo.PluginHost
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlin.coroutines.resume

// The implementation of package android on Android: Lanflow's VpnService
// (LanflowVpnService), started and stopped for Go.

fun makeVPN(): VPN = AndroidVPN()

private class AndroidVPN : VPN {
    override suspend fun prepare(): Boolean = withContext(Dispatchers.Main) {
        val intent = android.net.VpnService.prepare(Native.context)
        val granted = if (intent == null) {
            true
        } else {
            suspendCancellableCoroutine { k ->
                PluginHost.startForResult(intent) { code, _ -> if (k.isActive) k.resume(code == Activity.RESULT_OK) }
            }
        }
        if (granted && Build.VERSION.SDK_INT >= 33) {
            // The foreground notice; the VPN runs without it, so the start
            // does not wait for the answer (the core's start has a deadline).
            PluginHost.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS)) {}
        }
        granted
    }

    override suspend fun start(cfg: Config): Long {
        val pending = LanflowVpnService.request(cfg)
        val ctx = Native.context
        val intent = Intent(ctx, LanflowVpnService::class.java).setAction(LanflowVpnService.ACTION_START)
        withContext(Dispatchers.Main) {
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(intent) else ctx.startService(intent)
        }
        val pfd = try {
            withTimeout(15_000) { pending.await() }
        } catch (e: Throwable) {
            LanflowVpnService.cancel(pending)
            throw e
        }
        return pfd.detachFd().toLong()
    }

    override fun stop() {
        LanflowVpnService.stopAll()
    }

    override fun events(): Flow<String> = LanflowVpnService.events

    override fun nativeLibraryDir(): String = Native.context.applicationInfo.nativeLibraryDir

    override fun apkPath(): String = Native.context.applicationInfo.sourceDir
}
