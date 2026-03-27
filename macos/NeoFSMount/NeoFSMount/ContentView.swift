import Darwin
import FileProvider
import SwiftUI

struct ContentView: View {
    @State private var status: String = "Ready."
    @State private var neoInit: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("neoFS Mount")
                .font(.title2)
                .bold()
            Text("Native File Provider host. Containers appear in Finder under the NeoFS provider after you register the domain.")
                .font(.callout)
                .foregroundStyle(.secondary)
            Button("Register File Provider domain") {
                Task { await registerDomain() }
            }
            .keyboardShortcut(.defaultAction)
            Button("Connect NeoFS (from config.toml)") {
                connectFromConfig()
            }
            Text(status)
                .font(.footnote)
            if !neoInit.isEmpty {
                Text(neoInit)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(24)
        .frame(minWidth: 420, minHeight: 220)
    }

    @MainActor
    private func registerDomain() async {
        status = "Registering…"
        do {
            let domain = NSFileProviderDomain(
                identifier: NSFileProviderDomainIdentifier(rawValue: "org.neofs.mount.domain"),
                displayName: "NeoFS",
                pathRelativeToDocumentStorage: ""
            )
            try await NSFileProviderManager.add(domain)
            status = "Domain registered. Open Finder → NeoFS (or Locations)."
        } catch {
            status = "Failed: \(error.localizedDescription)"
        }
    }

    private func connectFromConfig() {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let cfgURL = home
            .appendingPathComponent("Library/Application Support/neofs-mount/config.toml", isDirectory: false)
        guard let raw = try? String(contentsOf: cfgURL, encoding: .utf8) else {
            neoInit = "No config at \(cfgURL.path)"
            return
        }
        guard let endpoint = parseTomlString(raw, key: "endpoint"),
              let wallet = parseTomlString(raw, key: "wallet_key") else {
            neoInit = "Could not parse endpoint / wallet_key in config.toml"
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
            neoInit = "NeoFS client ready (Go bridge)."
        } else {
            neoInit = "NeoFsFpInit failed: code \(code)"
        }
    }

    /// Minimal TOML string value parser for `key = "value"` / `key = 'value'`.
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
}
