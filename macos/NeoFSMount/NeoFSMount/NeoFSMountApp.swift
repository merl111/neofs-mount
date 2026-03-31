import Cocoa
import FileProvider
import SwiftUI

@main
struct NeoFSMountApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    
    var body: some Scene {
        // No window - this is a background helper app.
        // The tray app (neofs-mount-tray) is the user-facing UI.
        Settings {
            EmptyView()
        }
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    private let defaultConfigTemplate = """
    # neoFS-mount config (auto-created)
    #
    # Default location:
    #   macOS:  $HOME/Library/Application Support/neofs-mount/config.toml
    #
    # NeoFS endpoint, e.g. "s03.neofs.devenv:8080"
    endpoint = ""
    
    # Either a path to a file containing WIF, or a raw WIF string directly.
    wallet_key = ""
    
    # Linux-only (FUSE) directory mountpoint. On macOS the default integration is File Provider (Finder).
    mountpoint = "/tmp/neofs"
    
    read_only = false
    auto_mount = true
    run_at_login = false
    
    cache_dir = ""
    cache_size = 1073741824 # 1GiB
    
    log_level = "info"
    
    ignore_container_ids = []
    """
    
    private let domainID = NSFileProviderDomainIdentifier(rawValue: "org.neofs.mount.domain")

    func applicationDidFinishLaunching(_ notification: Notification) {
        terminateStaleInstances()

        Task {
            await setupFileProvider()
        }
    }

    private func terminateStaleInstances() {
        let myPID = ProcessInfo.processInfo.processIdentifier
        let myBundleID = Bundle.main.bundleIdentifier ?? "org.neofs.mount"

        for app in NSWorkspace.shared.runningApplications where app.bundleIdentifier == myBundleID {
            guard app.processIdentifier != myPID else { continue }
            NSLog("[NeoFS] Terminating stale instance (PID %d)", app.processIdentifier)
            app.terminate()
        }
    }

    @MainActor
    private func setupFileProvider() async {
        let domain = NSFileProviderDomain(identifier: domainID, displayName: "NeoFS")
        // We don't maintain a remote trash; deletes are permanent.
        if #available(macOS 13.0, *) {
            domain.supportsSyncingTrash = false
        }

        do {
            let existing = try await NSFileProviderManager.domains()
            if let old = existing.first(where: { $0.identifier == domainID }) {
                if #available(macOS 13.0, *), old.supportsSyncingTrash != false {
                    NSLog("[NeoFS] Removing domain to disable trash syncing")
                    try await NSFileProviderManager.remove(old)
                    try await NSFileProviderManager.add(domain)
                } else {
                    NSLog("[NeoFS] File Provider domain already registered")
                }
            } else {
                try await NSFileProviderManager.add(domain)
                NSLog("[NeoFS] File Provider domain registered")
            }
        } catch {
            NSLog("[NeoFS] Domain setup error: %@", error.localizedDescription)
        }

        connectFromConfig()

        if let manager = NSFileProviderManager(for: domain) {
            manager.reimportItems(below: .rootContainer) { _ in }
            manager.signalEnumerator(for: .rootContainer) { error in
                if let error = error {
                    NSLog("[NeoFS] signalEnumerator error: %@", error.localizedDescription)
                }
            }
            manager.signalEnumerator(for: .workingSet) { error in
                if let error = error {
                    NSLog("[NeoFS] signalEnumerator(workingSet) error: %@", error.localizedDescription)
                }
            }
        }
    }
    
    private func connectFromConfig() {
        // Use the App Group container so the sandboxed app and the tray share the same config.
        // Falls back to ~/Library/Application Support/... if the group container is unavailable.
        let cfgURL: URL
        if let groupURL = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "group.org.neofs.mount") {
            cfgURL = groupURL.appendingPathComponent("config.toml", isDirectory: false)
        } else {
            let home = FileManager.default.homeDirectoryForCurrentUser
            cfgURL = home.appendingPathComponent(
                "Library/Application Support/neofs-mount/config.toml",
                isDirectory: false
            )
        }
        
        // Ensure config exists
        let raw: String
        do {
            raw = try ensureConfigAndRead(cfgURL: cfgURL)
        } catch {
            NSLog("[NeoFS] Config error: %@", error.localizedDescription)
            return
        }
        
        guard let endpoint = parseTomlString(raw, key: "endpoint"),
              let wallet = parseTomlString(raw, key: "wallet_key"),
              !endpoint.isEmpty,
              !wallet.isEmpty else {
            NSLog("[NeoFS] Waiting for config: endpoint and wallet_key not set in %@", cfgURL.path)
            return
        }
        
        let ep = strdup(endpoint)
        let wk = strdup(wallet)
        defer {
            free(ep)
            free(wk)
        }
        
        let code = NeoFsFpInit(ep, wk)
        if code == 0 {
            NSLog("[NeoFS] NeoFS client connected (endpoint: %@)", endpoint)
        } else {
            NSLog("[NeoFS] NeoFsFpInit failed: code %d", code)
        }
    }
    
    private func ensureConfigAndRead(cfgURL: URL) throws -> String {
        if FileManager.default.fileExists(atPath: cfgURL.path) {
            return try String(contentsOf: cfgURL, encoding: .utf8)
        }
        
        let dir = cfgURL.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try defaultConfigTemplate.write(to: cfgURL, atomically: true, encoding: .utf8)
        NSLog("[NeoFS] Created default config at %@", cfgURL.path)
        return try String(contentsOf: cfgURL, encoding: .utf8)
    }
    
    private func parseTomlString(_ toml: String, key: String) -> String? {
        for line in toml.split(separator: "\n", omittingEmptySubsequences: false) {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.first != "#" else { continue }
            let prefix = "\(key) = "
            guard t.hasPrefix(prefix) else { continue }
            let rest = String(t.dropFirst(prefix.count)).trimmingCharacters(in: .whitespaces)
            if rest.hasPrefix("\""), rest.hasSuffix("\""), rest.count >= 2 {
                return String(rest.dropFirst().dropLast())
            }
            if rest.hasPrefix("'"), rest.hasSuffix("'"), rest.count >= 2 {
                return String(rest.dropFirst().dropLast())
            }
            return rest
        }
        return nil
    }
    
    func applicationWillTerminate(_ notification: Notification) {
        let sem = DispatchSemaphore(value: 0)
        Task {
            do {
                let domains = try await NSFileProviderManager.domains()
                for d in domains where d.identifier == domainID {
                    try await NSFileProviderManager.remove(d)
                    NSLog("[NeoFS] Removed File Provider domain on shutdown")
                }
            } catch {
                NSLog("[NeoFS] Domain removal error: %@", error.localizedDescription)
            }
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 3)

        NeoFsFpShutdown()
        NSLog("[NeoFS] Shutdown complete")
    }
}
