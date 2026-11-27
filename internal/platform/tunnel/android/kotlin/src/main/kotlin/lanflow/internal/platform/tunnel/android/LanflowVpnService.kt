package lanflow.internal.platform.tunnel.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.IpPrefix
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import java.net.InetAddress

/**
 * Lanflow's VPN: a foreground service whose interface Mihomo serves as its
 * TUN. Go asks for it through AndroidVPN, which starts the service and
 * waits for the descriptor that onStartCommand establishes.
 *
 * The app itself is left out of the VPN (addDisallowedApplication): Mihomo
 * runs as a program of the app, under its user id, and its connections to
 * the proxies must not loop back into the VPN. A program cannot call
 * protect() on its sockets, which only this process can.
 */
class LanflowVpnService : VpnService() {
    companion object {
        const val ACTION_START = "lanflow.vpn.START"
        private const val CHANNEL = "lanflow.vpn"
        private const val NOTICE = 0x4c46

        private val lock = Any()
        private var config: Config? = null
        private var pending: CompletableDeferred<ParcelFileDescriptor>? = null
        private var instance: LanflowVpnService? = null

        private val _events = MutableSharedFlow<String>(extraBufferCapacity = 8)

        /** Why the VPN went down without stopAll: "revoked" or "destroyed". */
        val events: SharedFlow<String> get() = _events

        /** Sets the VPN the next start establishes, and the descriptor it will answer with. */
        fun request(cfg: Config): CompletableDeferred<ParcelFileDescriptor> = synchronized(lock) {
            pending?.cancel()
            val d = CompletableDeferred<ParcelFileDescriptor>()
            config = cfg
            pending = d
            d
        }

        /** Forgets a request that timed out or was canceled. */
        fun cancel(d: CompletableDeferred<ParcelFileDescriptor>) = synchronized(lock) {
            if (pending === d) {
                pending = null
                config = null
            }
            if (d.isCompleted && !d.isCancelled) runCatching { d.getCompleted().close() }
            d.cancel()
        }

        /** Takes the VPN down, as asked: no event follows. */
        fun stopAll() {
            val s = synchronized(lock) {
                instance?.asked = true
                pending?.cancel()
                pending = null
                config = null
                instance
            } ?: return
            Handler(Looper.getMainLooper()).post { s.finish() }
        }
    }

    /**
     * This instance is being stopped as Go asked: its end is no event. Per
     * instance, as a stopped service may end after the next one started.
     */
    @Volatile
    var asked = false

    override fun onCreate() {
        super.onCreate()
        synchronized(lock) { instance = this }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // A foreground service must say so within seconds of its start.
        foreground()
        asked = false
        if (intent?.action != ACTION_START) {
            // Restarted by the system, or the always-on VPN setting: Go
            // starts the VPN with the core, not the service alone.
            finish()
            return START_NOT_STICKY
        }
        val (cfg, d) = synchronized(lock) { config to pending }
        if (cfg == null || d == null || d.isCompleted) return START_NOT_STICKY
        try {
            val pfd = establish(cfg) ?: throw IllegalStateException("VPN permission was not granted")
            if (!d.complete(pfd)) pfd.close()
        } catch (e: Throwable) {
            d.completeExceptionally(e)
            finish()
        }
        return START_NOT_STICKY
    }

    private fun establish(cfg: Config): ParcelFileDescriptor? {
        val b = Builder().setSession(cfg.session).setMtu(cfg.mtu.toInt())
        val (a4, p4) = prefix(cfg.address4)
        b.addAddress(a4, p4)
        b.addRoute("0.0.0.0", 0)
        if (cfg.address6.isNotEmpty()) {
            val (a6, p6) = prefix(cfg.address6)
            b.addAddress(a6, p6)
            b.addRoute("::", 0)
        }
        for (d in cfg.dns) b.addDnsServer(d)
        if (Build.VERSION.SDK_INT >= 33) {
            for (x in cfg.exclude) {
                val (a, p) = prefix(x)
                runCatching { b.excludeRoute(IpPrefix(InetAddress.getByName(a), p)) }
            }
        }
        b.addDisallowedApplication(packageName)
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        launchIntent()?.let { b.setConfigureIntent(it) }
        return b.establish()
    }

    private fun prefix(s: String): Pair<String, Int> {
        val i = s.lastIndexOf('/')
        require(i > 0) { "not a prefix: $s" }
        return s.substring(0, i) to s.substring(i + 1).toInt()
    }

    private fun launchIntent(): PendingIntent? {
        val i = packageManager.getLaunchIntentForPackage(packageName) ?: return null
        return PendingIntent.getActivity(this, 0, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun foreground() {
        val nm = getSystemService(NotificationManager::class.java)
        val title = applicationInfo.loadLabel(packageManager).toString()
        val n = if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "VPN", NotificationManager.IMPORTANCE_LOW))
            Notification.Builder(this, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }.setSmallIcon(applicationInfo.icon)
            .setContentTitle(title)
            .setContentText("VPN")
            .setOngoing(true)
            .setContentIntent(launchIntent())
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTICE, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTICE, n)
        }
    }

    fun finish() {
        if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE) else @Suppress("DEPRECATION") stopForeground(true)
        stopSelf()
    }

    override fun onRevoke() {
        // The user turned the VPN off in the system's settings, or another
        // app's VPN took over.
        if (!asked) _events.tryEmit("revoked")
        asked = true
        finish()
    }

    override fun onDestroy() {
        synchronized(lock) { if (instance === this) instance = null }
        if (!asked) _events.tryEmit("destroyed")
        super.onDestroy()
    }
}
