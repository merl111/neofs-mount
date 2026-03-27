import FileProvider
import Foundation
import os.log
import UniformTypeIdentifiers

/// `NSFileProviderReplicatedExtension` is a protocol (not a class); the implementation must inherit `NSObject`.
/// Method signatures follow the macOS 15 / Xcode 16 SDK (completion handlers + `NSFileProviderRequest`).
final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    private let logger = Logger(subsystem: "org.neofs.mount", category: "FileProvider")

    required init(domain: NSFileProviderDomain) {
        super.init()
        let v = NeoFsFpVersion()
        logger.info("NeoFsFpVersion \(v, privacy: .public)")
    }

    func invalidate() {
        NeoFsFpShutdown()
    }

    // MARK: - NSFileProviderEnumerating

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest) throws -> any NSFileProviderEnumerator {
        EmptyEnumerator()
    }

    // MARK: - Metadata & content

    func item(
        for identifier: NSFileProviderItemIdentifier,
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, (any Error)?) -> Void
    ) -> Progress {
        if identifier == .rootContainer {
            completionHandler(RootFolderItem(), nil)
        } else {
            completionHandler(
                nil,
                NSError(
                    domain: NSCocoaErrorDomain,
                    code: NSFileNoSuchFileError,
                    userInfo: [NSLocalizedDescriptionKey: "Item not implemented yet"]
                )
            )
        }
        return Self.finishedProgress()
    }

    func fetchContents(
        for itemIdentifier: NSFileProviderItemIdentifier,
        version requestedVersion: NSFileProviderItemVersion?,
        request: NSFileProviderRequest,
        completionHandler: @escaping (URL?, NSFileProviderItem?, (any Error)?) -> Void
    ) -> Progress {
        completionHandler(
            nil,
            nil,
            NSError(
                domain: NSCocoaErrorDomain,
                code: NSFileReadNoSuchFileError,
                userInfo: [NSLocalizedDescriptionKey: "Fetch contents not implemented yet"]
            )
        )
        return Self.finishedProgress()
    }

    // MARK: - Mutations (stubs)

    func createItem(
        basedOn itemTemplate: NSFileProviderItem,
        fields: NSFileProviderItemFields,
        contents url: URL?,
        options: NSFileProviderCreateItemOptions = [],
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, (any Error)?) -> Void
    ) -> Progress {
        completionHandler(nil, [], false, NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError))
        return Self.finishedProgress()
    }

    func modifyItem(
        _ item: NSFileProviderItem,
        baseVersion version: NSFileProviderItemVersion,
        changedFields: NSFileProviderItemFields,
        contents newContents: URL?,
        options: NSFileProviderModifyItemOptions = [],
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, (any Error)?) -> Void
    ) -> Progress {
        completionHandler(nil, [], false, NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError))
        return Self.finishedProgress()
    }

    func deleteItem(
        identifier: NSFileProviderItemIdentifier,
        baseVersion version: NSFileProviderItemVersion,
        options: NSFileProviderDeleteItemOptions = [],
        request: NSFileProviderRequest,
        completionHandler: @escaping ((any Error)?) -> Void
    ) -> Progress {
        completionHandler(NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError))
        return Self.finishedProgress()
    }

    private static func finishedProgress() -> Progress {
        let p = Progress(totalUnitCount: 1)
        p.completedUnitCount = 1
        return p
    }
}

// MARK: - Items & enumerator

final class RootFolderItem: NSObject, NSFileProviderItem {
    var itemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var parentItemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var filename: String { "NeoFS" }
    var contentType: UTType { .folder }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsWriting, .allowsContentEnumerating, .allowsReparenting] }
}

final class EmptyEnumerator: NSObject, NSFileProviderEnumerator {
    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        observer.finishEnumerating(upTo: nil)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(nil)
    }
}
