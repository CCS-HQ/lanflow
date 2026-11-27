// Lanflow's packet tunnel: Mihomo (tunnelcore, a Go c-archive) running in
// the extension's process on the utun descriptor NetworkExtension opens.
//
// The app and the extension share an App Group container (its identifier
// is LanflowAppGroup in both Info.plists):
//
//   <group>/core/        Mihomo's home: geodata, providers, cache.db,
//                        source.yaml (from the app), config.yaml (staged
//                        here), mihomo.log
//   <group>/c.sock       the controller's unix socket, which the app dials
//
// The app hands its runtime config over as source.yaml, then starts the
// tunnel; before a hot reload it writes source.yaml again and sends the
// message {"op":"stage"}, which answers the path to PUT to /configs.

import Foundation
import NetworkExtension

final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var tunFD: Int32 = 0

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        guard let paths = CorePaths.current else {
            completionHandler(TunnelError("App Group container unavailable: is the extension signed with the app's group?"))
            return
        }
        // Staged without a descriptor first, for the addresses the network
        // settings must match.
        let staged = Core.stage(paths, fd: 0)
        if let message = staged["error"] as? String {
            completionHandler(TunnelError(message))
            return
        }
        guard let network = staged["network"] as? [String: Any] else {
            completionHandler(TunnelError("the core returned no network"))
            return
        }
        setTunnelNetworkSettings(Self.settings(network)) { [weak self] error in
            guard let self else { return }
            if let error {
                completionHandler(error)
                return
            }
            let fd = LanflowCoreTunFD()
            guard fd > 0 else {
                completionHandler(TunnelError("cannot find the tunnel's utun descriptor"))
                return
            }
            self.tunFD = fd
            let failure = Core.start(paths, fd: fd)
            completionHandler(failure.isEmpty ? nil : TunnelError(failure))
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        LanflowCoreStop()
        tunFD = 0
        completionHandler()
    }

    /// Messages from the app (NETunnelProviderSession.sendProviderMessage),
    /// JSON in and out: {"op":"stage"} → {"path"} or {"error"};
    /// {"op":"memory"} → the Go runtime's memory; {"op":"version"}.
    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        let request = (try? JSONSerialization.jsonObject(with: messageData)) as? [String: Any] ?? [:]
        var answer: [String: Any]
        switch request["op"] as? String {
        case "stage":
            if let paths = CorePaths.current {
                answer = Core.stage(paths, fd: tunFD)
                answer.removeValue(forKey: "network")
            } else {
                answer = ["error": "App Group container unavailable"]
            }
        case "memory":
            answer = Core.json(LanflowCoreMemory())
        case "version":
            answer = ["version": Core.take(LanflowCoreVersion())]
        default:
            answer = ["error": "unknown message"]
        }
        completionHandler?(try? JSONSerialization.data(withJSONObject: answer))
    }

    /// The tunnel's network: the TUN addresses Mihomo derives from the
    /// config, every route, and a DNS server inside the TUN that Mihomo's
    /// dns-hijack answers.
    static func settings(_ network: [String: Any]) -> NEPacketTunnelNetworkSettings {
        let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        let inet4 = network["inet4"] as? String ?? "198.18.0.1"
        let v4 = NEIPv4Settings(addresses: [inet4], subnetMasks: ["255.255.255.252"])
        v4.includedRoutes = [NEIPv4Route.default()]
        s.ipv4Settings = v4
        if let inet6 = network["inet6"] as? String, !inet6.isEmpty {
            let v6 = NEIPv6Settings(addresses: [inet6], networkPrefixLengths: [126])
            v6.includedRoutes = [NEIPv6Route.default()]
            s.ipv6Settings = v6
        }
        let dns = NEDNSSettings(servers: [network["dns"] as? String ?? "198.18.0.2"])
        dns.matchDomains = [""]
        s.dnsSettings = dns
        if let mtu = network["mtu"] as? Int {
            s.mtu = NSNumber(value: mtu)
        }
        return s
    }
}

struct TunnelError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

/// Where the core keeps its files, in the App Group container.
struct CorePaths {
    let home: String
    let controller: String

    static var current: CorePaths? {
        let group = Bundle.main.object(forInfoDictionaryKey: "LanflowAppGroup") as? String ?? ""
        guard !group.isEmpty,
              let root = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group)
        else { return nil }
        let home = root.appendingPathComponent("core", isDirectory: true)
        try? FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        return CorePaths(home: home.path, controller: root.appendingPathComponent("c.sock").path)
    }
}

/// Calls into tunnelcore, which takes C strings and returns ones to free.
enum Core {
    /// A C string for a call, which the caller frees; tunnelcore copies it.
    static func cstr(_ s: String) -> UnsafeMutablePointer<CChar> {
        strdup(s)
    }

    static func take(_ p: UnsafeMutablePointer<CChar>?) -> String {
        guard let p else { return "" }
        defer { LanflowCoreFree(p) }
        return String(cString: p)
    }

    static func json(_ p: UnsafeMutablePointer<CChar>?) -> [String: Any] {
        let s = take(p)
        return (try? JSONSerialization.jsonObject(with: Data(s.utf8))) as? [String: Any] ?? ["error": s]
    }

    static func stage(_ paths: CorePaths, fd: Int32) -> [String: Any] {
        let home = cstr(paths.home), controller = cstr(paths.controller)
        defer { free(home); free(controller) }
        return json(LanflowCoreStage(home, controller, fd))
    }

    /// Runs the core on fd; "" once it runs, else why not.
    static func start(_ paths: CorePaths, fd: Int32) -> String {
        let home = cstr(paths.home), controller = cstr(paths.controller)
        defer { free(home); free(controller) }
        return take(LanflowCoreStart(home, controller, fd))
    }
}
