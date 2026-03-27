import FileProvider
import Foundation
import os.log

@objc(FileProviderExtension)
final class FileProviderExtension: NSFileProviderReplicatedExtension {
    private let logger = Logger(subsystem: "org.neofs.mount", category: "FileProvider")

    required init(domain: NSFileProviderDomain) {
        super.init(domain: domain)
        let v = NeoFsFpVersion()
        logger.info("NeoFsFpVersion \(v, privacy: .public)")
    }

    func invalidate() {
        NeoFsFpShutdown()
    }

    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderItemRequest) async throws -> NSFileProviderItem {
        if identifier == .rootContainer {
            return RootFolderItem()
        }
        throw NSError(
            domain: NSCocoaErrorDomain,
            code: NSFileNoSuchFileError,
            userInfo: [NSLocalizedDescriptionKey: "Item not implemented yet"]
        )
    }

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier, request: NSFileProviderEnumerationRequest) async throws -> NSFileProviderEnumerator {
        EmptyEnumerator()
    }

    func fetchContents(for itemIdentifier: NSFileProviderItemIdentifier, version: NSFileProviderItemVersion?, request: NSFileProviderContentFetchingRequest) async throws -> NSFileProviderItem {
        throw NSError(
            domain: NSCocoaErrorDomain,
            code: NSFileReadNoSuchFileError,
            userInfo: [NSLocalizedDescriptionKey: "Fetch contents not implemented yet"]
        )
    }
}

// MARK: - Items & enumerator

final class RootFolderItem: NSObject, NSFileProviderItem {
    var itemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var parentItemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var filename: String { "NeoFS" }
    var typeIdentifier: String { "public.folder" }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsWriting, .allowsContentEnumerating, .allowsReparenting] }
}

final class EmptyEnumerator: NSObject, NSFileProviderEnumerator {
    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        observer.finishEnumerating(upTo: nil)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(nil)
    }
}
