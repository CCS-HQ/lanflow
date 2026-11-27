import Foundation
import NetworkExtension

/// The system VPN of Lanflow's packet tunnel extension
/// (<app>/PlugIns/PacketTunnel.appex), for package iosvpn: one
/// NETunnelProviderManager, made on the first start (iOS then asks the
/// user to allow the VPN), started, stopped and messaged.
///
/// Methods: "start", "stop", "status" → {"status"}, "message" {"data":
/// JSON text} → {"data": JSON text}. Event "status" {"status", "error"?}
/// on every change. The App Group paths Go finds itself (group_ios.go):
/// the service needs them before the app runs.
final class LanflowTunnelPlugin: MyGoPlugin {
    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?

    private var providerID: String { (Bundle.main.bundleIdentifier ?? "") + ".PacketTunnel" }

    func call(_ method: String, _ args: [String: Any], _ reply: @escaping PluginReply) {
        switch method {
        case "status":
            load(create: false) { manager, _ in
                reply(.success(["status": Self.name(manager?.connection.status ?? .invalid)]))
            }
        case "start":
            load(create: true) { manager, error in
                guard let manager else {
                    reply(.failure(PluginFailure("Config", error?.localizedDescription ?? "")))
                    return
                }
                do {
                    try manager.connection.startVPNTunnel(options: nil)
                    reply(.success(nil))
                } catch {
                    reply(.failure(PluginFailure("Start", error.localizedDescription)))
                }
            }
        case "stop":
            load(create: false) { manager, _ in
                manager?.connection.stopVPNTunnel()
                reply(.success(nil))
            }
        case "message":
            let text = args["data"] as? String ?? "{}"
            load(create: false) { manager, _ in
                guard let session = manager?.connection as? NETunnelProviderSession,
                      session.status == .connected || session.status == .reasserting else {
                    reply(.failure(PluginFailure("NotRunning", "the VPN is not connected")))
                    return
                }
                do {
                    try session.sendProviderMessage(Data(text.utf8)) { data in
                        guard let data else {
                            reply(.failure(PluginFailure("NoAnswer", "the tunnel did not answer")))
                            return
                        }
                        reply(.success(["data": String(decoding: data, as: UTF8.self)]))
                    }
                } catch {
                    reply(.failure(PluginFailure("Message", error.localizedDescription)))
                }
            }
        default:
            reply(.failure(PluginFailure("NoMethod", method)))
        }
    }

    /// load finds Lanflow's VPN configuration, making and saving it when
    /// create is set (the first time iOS asks the user to allow it), and
    /// watches its status.
    private func load(create: Bool, _ done: @escaping (NETunnelProviderManager?, Error?) -> Void) {
        NETunnelProviderManager.loadAllFromPreferences { [weak self] managers, error in
            DispatchQueue.main.async {
                guard let self else { return }
                if let error {
                    done(nil, error)
                    return
                }
                let existing = managers?.first {
                    ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == self.providerID
                }
                if let existing, existing.isEnabled || !create {
                    self.watch(existing)
                    done(existing, nil)
                    return
                }
                guard create else {
                    done(nil, nil)
                    return
                }
                let m = existing ?? NETunnelProviderManager()
                let proto = NETunnelProviderProtocol()
                proto.providerBundleIdentifier = self.providerID
                proto.serverAddress = "Lanflow"
                proto.disconnectOnSleep = false
                m.protocolConfiguration = proto
                m.localizedDescription = "Lanflow"
                m.isEnabled = true
                m.saveToPreferences { error in
                    if let error {
                        DispatchQueue.main.async { done(nil, error) }
                        return
                    }
                    // A saved configuration must be loaded again before
                    // it starts.
                    m.loadFromPreferences { error in
                        DispatchQueue.main.async {
                            self.watch(m)
                            done(error == nil ? m : nil, error)
                        }
                    }
                }
            }
        }
    }

    private func watch(_ m: NETunnelProviderManager) {
        if manager === m, observer != nil { return }
        if let observer { NotificationCenter.default.removeObserver(observer) }
        manager = m
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange, object: m.connection, queue: .main
        ) { [weak self] _ in
            self?.statusChanged(m.connection)
        }
    }

    private func statusChanged(_ connection: NEVPNConnection) {
        let status = connection.status
        guard status == .disconnected || status == .invalid else {
            PluginHost.emit("lanflowtunnel", "status", ["status": Self.name(status)])
            return
        }
        // Why it went down: the extension's start error, the user turning
        // it off in Settings, another VPN taking over.
        connection.fetchLastDisconnectError { error in
            DispatchQueue.main.async {
                var event: [String: Any] = ["status": Self.name(status)]
                if let error { event["error"] = error.localizedDescription }
                PluginHost.emit("lanflowtunnel", "status", event)
            }
        }
    }

    static func name(_ s: NEVPNStatus) -> String {
        switch s {
        case .invalid: return "invalid"
        case .disconnected: return "disconnected"
        case .connecting: return "connecting"
        case .connected: return "connected"
        case .reasserting: return "reasserting"
        case .disconnecting: return "disconnecting"
        @unknown default: return "invalid"
        }
    }
}
