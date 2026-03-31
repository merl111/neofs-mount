import FileProvider
import Foundation
import os.log
import UniformTypeIdentifiers

private let logger = Logger(subsystem: "org.neofs.mount", category: "FileProvider")
private let appGroupID = "group.org.neofs.mount"

// MARK: - Identifier helpers

// Identifier scheme:
//   .rootContainer                       -> top-level (lists containers)
//   container:<cid>                      -> container root (lists top-level objects)
//   container:<cid>:dir:<relative-path>  -> subdirectory inside a container
//   container:<cid>:obj:<oid>            -> file (NeoFS object)

private func containerIdentifier(_ cid: String) -> NSFileProviderItemIdentifier {
    NSFileProviderItemIdentifier("container:\(cid)")
}

private func dirIdentifier(_ cid: String, path: String) -> NSFileProviderItemIdentifier {
    NSFileProviderItemIdentifier("container:\(cid):dir:\(path)")
}

private func objIdentifier(_ cid: String, oid: String) -> NSFileProviderItemIdentifier {
    NSFileProviderItemIdentifier("container:\(cid):obj:\(oid)")
}

private enum ParsedID {
    case root
    case trash
    case container(cid: String)
    case directory(cid: String, path: String)
    case object(cid: String, oid: String)
}

private func parseIdentifier(_ id: NSFileProviderItemIdentifier) -> ParsedID {
    if id == .rootContainer { return .root }
    if id == .trashContainer { return .trash }

    let raw = id.rawValue
    guard raw.hasPrefix("container:") else { return .root }

    let afterContainer = String(raw.dropFirst("container:".count))

    if let dirRange = afterContainer.range(of: ":dir:") {
        let cid = String(afterContainer[afterContainer.startIndex..<dirRange.lowerBound])
        let path = String(afterContainer[dirRange.upperBound...])
        return .directory(cid: cid, path: path)
    }

    if let objRange = afterContainer.range(of: ":obj:") {
        let cid = String(afterContainer[afterContainer.startIndex..<objRange.lowerBound])
        let oid = String(afterContainer[objRange.upperBound...])
        return .object(cid: cid, oid: oid)
    }

    return .container(cid: afterContainer)
}

// MARK: - Bridge helpers

private func resolveConfigPath() -> String {
    if let groupURL = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroupID) {
        return groupURL.appendingPathComponent("config.toml").path
    }
    let home = NSHomeDirectory()
    return "\(home)/Library/Group Containers/\(appGroupID)/config.toml"
}

private func ensureClient() {
    let path = resolveConfigPath()
    let exists = FileManager.default.fileExists(atPath: path)
    logger.info("ensureClient configPath=\(path, privacy: .public) exists=\(exists, privacy: .public)")

    if !exists {
        logger.error("Config file not found, cannot init NeoFS client")
        return
    }

    for attempt in 1...3 {
        let cPath = strdup(path)
        let code = NeoFsFpEnsureClient(cPath)
        free(cPath)
        if code == 0 {
            logger.info("NeoFS client ready")
            return
        }
        logger.error("NeoFsFpEnsureClient attempt \(attempt, privacy: .public) failed: \(code, privacy: .public)")
        if attempt < 3 {
            NeoFsFpShutdown()
            Thread.sleep(forTimeInterval: Double(attempt))
        }
    }
}

struct ContainerJSON: Decodable {
    let id: String
    let name: String
}

struct DirEntryJSON: Decodable {
    let name: String
    let objectID: String?
    let size: Int64
    let isDirectory: Bool
}

func listContainersFromBridge() -> [ContainerJSON] {
    ensureClient()
    guard let cstr = NeoFsFpListContainers() else { return [] }
    defer { free(cstr) }
    let json = String(cString: cstr)
    guard let data = json.data(using: .utf8),
          let result = try? JSONDecoder().decode([ContainerJSON].self, from: data) else { return [] }
    return result
}

func listEntriesFromBridge(containerID: String, prefix: String) -> [DirEntryJSON] {
    ensureClient()
    let cidStr = strdup(containerID)
    let pfxStr = strdup(prefix)
    defer {
        free(cidStr)
        free(pfxStr)
    }
    guard let cstr = NeoFsFpListEntries(cidStr, pfxStr) else { return [] }
    defer { free(cstr) }
    let json = String(cString: cstr)
    guard let data = json.data(using: .utf8),
          let result = try? JSONDecoder().decode([DirEntryJSON].self, from: data) else { return [] }
    return result
}

// MARK: - File Provider Extension

final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    let fpDomain: NSFileProviderDomain

    required init(domain: NSFileProviderDomain) {
        self.fpDomain = domain
        super.init()
        logger.info("FileProviderExtension init, bridge version \(NeoFsFpVersion(), privacy: .public)")

        if let groupURL = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroupID) {
            let tmpDir = groupURL.appendingPathComponent("tmp", isDirectory: true)
            try? FileManager.default.createDirectory(at: tmpDir, withIntermediateDirectories: true)
            let cDir = strdup(tmpDir.path)
            NeoFsFpSetTempDir(cDir)
            free(cDir)
            logger.info("Set temp dir to \(tmpDir.path, privacy: .public)")
        }

        ensureClient()
    }

    func invalidate() {
        // Don't call NeoFsFpShutdown() here: fileproviderd reuses the same
        // process for multiple extension lifecycles, so the Go client should
        // stay alive across invalidate/re-init cycles.
    }

    // MARK: - Enumeration

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest) throws -> any NSFileProviderEnumerator {
        logger.info("enumerator(for: \(containerItemIdentifier.rawValue, privacy: .public))")

        if containerItemIdentifier == .workingSet {
            return WorkingSetEnumerator()
        }
        if containerItemIdentifier == .trashContainer {
            return EmptyEnumerator()
        }

        switch parseIdentifier(containerItemIdentifier) {
        case .root:
            return ContainerEnumerator()
        case .trash:
            return EmptyEnumerator()
        case .container(let cid):
            return ObjectEnumerator(containerID: cid, prefix: "")
        case .directory(let cid, let path):
            return ObjectEnumerator(containerID: cid, prefix: path)
        case .object:
            return EmptyEnumerator()
        }
    }

    // MARK: - Item lookup

    func item(
        for identifier: NSFileProviderItemIdentifier,
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, (any Error)?) -> Void
    ) -> Progress {
        switch parseIdentifier(identifier) {
        case .root:
            completionHandler(RootItem(), nil)
        case .trash:
            completionHandler(TrashItem(), nil)
        case .container(let cid):
            let containers = listContainersFromBridge()
            if let c = containers.first(where: { $0.id == cid }) {
                completionHandler(ContainerItem(info: c), nil)
            } else {
                completionHandler(ContainerItem(info: ContainerJSON(id: cid, name: cid)), nil)
            }
        case .directory(let cid, let path):
            let name = (path as NSString).lastPathComponent
            completionHandler(DirectoryItem(containerID: cid, path: path, name: name), nil)
        case .object(let cid, let oid):
            let entries = listEntriesFromBridge(containerID: cid, prefix: "")
            if let e = entries.first(where: { $0.objectID == oid }) {
                completionHandler(ObjectItem(containerID: cid, entry: e), nil)
            } else {
                completionHandler(nil, NSFileProviderError(.noSuchItem))
            }
        }
        return Self.finishedProgress()
    }

    // MARK: - Fetch contents

    func fetchContents(
        for itemIdentifier: NSFileProviderItemIdentifier,
        version requestedVersion: NSFileProviderItemVersion?,
        request: NSFileProviderRequest,
        completionHandler: @escaping (URL?, NSFileProviderItem?, (any Error)?) -> Void
    ) -> Progress {
        guard case .object(let cid, let oid) = parseIdentifier(itemIdentifier) else {
            logger.error("fetchContents: identifier is not an object: \(itemIdentifier.rawValue, privacy: .public)")
            completionHandler(nil, nil, NSFileProviderError(.noSuchItem))
            return Self.finishedProgress()
        }

        logger.info("fetchContents: cid=\(cid, privacy: .public) oid=\(oid, privacy: .public)")
        ensureClient()

        var tmpPath: String? = nil
        for attempt in 1...2 {
            let cidStr = strdup(cid)
            let oidStr = strdup(oid)
            let result = NeoFsFpFetchObject(cidStr, oidStr)
            free(cidStr)
            free(oidStr)

            if let pathCStr = result {
                tmpPath = String(cString: pathCStr)
                free(pathCStr)
                break
            }

            logger.error("fetchContents: attempt \(attempt, privacy: .public) failed for oid=\(oid, privacy: .public)")
            if attempt < 2 {
                NeoFsFpShutdown()
                Thread.sleep(forTimeInterval: 1)
                ensureClient()
            }
        }

        guard let downloadedPath = tmpPath else {
            completionHandler(nil, nil, NSFileProviderError(.serverUnreachable))
            return Self.finishedProgress()
        }
        logger.info("fetchContents: downloaded to \(downloadedPath, privacy: .public)")

        let url = URL(fileURLWithPath: downloadedPath)
        let entries = listEntriesFromBridge(containerID: cid, prefix: "")
        let entry = entries.first(where: { $0.objectID == oid })
        let item: NSFileProviderItem = entry.map { ObjectItem(containerID: cid, entry: $0) }
            ?? SimpleObjectItem(containerID: cid, objectID: oid)

        completionHandler(url, item, nil)
        return Self.finishedProgress()
    }

    // MARK: - Mutations (read-only stubs)

    func createItem(
        basedOn itemTemplate: NSFileProviderItem,
        fields: NSFileProviderItemFields,
        contents url: URL?,
        options: NSFileProviderCreateItemOptions = [],
        request: NSFileProviderRequest,
        completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, (any Error)?) -> Void
    ) -> Progress {
        // fileproviderd may call createItemBasedOnTemplate during disk import / reconciliation
        // to materialize placeholders. We don't support user-driven uploads/creates yet, but
        // we must not block the system's internal bookkeeping.
        if url == nil {
            if itemTemplate.itemIdentifier == .trashContainer || itemTemplate.parentItemIdentifier == .trashContainer {
                completionHandler(TrashItem(), [], false, nil)
            } else {
                completionHandler(itemTemplate, [], false, nil)
            }
            return Self.finishedProgress()
        }

        // Allow fileproviderd to create internal placeholders during reconciliation.
        if itemTemplate.itemIdentifier.rawValue.hasPrefix("__fp/") {
            completionHandler(itemTemplate, [], false, nil)
            return Self.finishedProgress()
        }

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
        // Treat "move to trash" as a permanent delete.
        if changedFields.contains(.parentItemIdentifier),
           item.parentItemIdentifier == .trashContainer,
           case .object(let cid, let oid) = parseIdentifier(item.itemIdentifier) {
            logger.info("modifyItem (trash->delete): cid=\(cid, privacy: .public) oid=\(oid, privacy: .public)")

            ensureClient()
            let cidStr = strdup(cid)
            let oidStr = strdup(oid)
            let rc = NeoFsFpDeleteObject(cidStr, oidStr)
            free(cidStr)
            free(oidStr)

            if rc == 0 {
                completionHandler(nil, [], false, nil)
            } else {
                logger.error("modifyItem (trash->delete): delete failed rc=\(rc, privacy: .public)")
                completionHandler(nil, [], false, NSFileProviderError(.serverUnreachable))
            }
            return Self.finishedProgress()
        }

        // Allow fileproviderd to update its internal placeholders without involving NeoFS.
        // These identifiers are not part of our public identifier scheme.
        if item.itemIdentifier.rawValue.hasPrefix("__fp/") {
            completionHandler(item, [], false, nil)
            return Self.finishedProgress()
        }

        // fileproviderd may issue metadata-only modifications during disk import / reconciliation.
        // Allow those to succeed (we don't persist them remotely).
        if newContents == nil {
            completionHandler(item, [], false, nil)
            return Self.finishedProgress()
        }

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
        guard case .object(let cid, let oid) = parseIdentifier(identifier) else {
            completionHandler(NSFileProviderError(.noSuchItem))
            return Self.finishedProgress()
        }

        logger.info("deleteItem: cid=\(cid, privacy: .public) oid=\(oid, privacy: .public)")
        ensureClient()

        let cidStr = strdup(cid)
        let oidStr = strdup(oid)
        let rc = NeoFsFpDeleteObject(cidStr, oidStr)
        free(cidStr)
        free(oidStr)

        if rc == 0 {
            completionHandler(nil)
        } else {
            logger.error("deleteItem: NeoFsFpDeleteObject failed rc=\(rc, privacy: .public)")
            completionHandler(NSFileProviderError(.serverUnreachable))
        }
        return Self.finishedProgress()
    }

    private static func finishedProgress() -> Progress {
        let p = Progress(totalUnitCount: 1)
        p.completedUnitCount = 1
        return p
    }
}

// MARK: - Item types

private let itemVersion = NSFileProviderItemVersion(
    contentVersion: "v1".data(using: .utf8)!,
    metadataVersion: "v1".data(using: .utf8)!
)

final class RootItem: NSObject, NSFileProviderItem {
    var itemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var parentItemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var filename: String { "NeoFS" }
    var contentType: UTType { .folder }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsContentEnumerating] }
    var fileSystemFlags: NSFileProviderFileSystemFlags { [.userReadable, .userWritable, .userExecutable] }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

private let FileProviderExtension_itemVersion = itemVersion

final class TrashItem: NSObject, NSFileProviderItem {
    var itemIdentifier: NSFileProviderItemIdentifier { .trashContainer }
    var parentItemIdentifier: NSFileProviderItemIdentifier { .trashContainer }
    var filename: String { ".Trash" }
    var contentType: UTType { .folder }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsContentEnumerating] }
    var fileSystemFlags: NSFileProviderFileSystemFlags { [.userReadable, .userWritable, .userExecutable] }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

final class ContainerItem: NSObject, NSFileProviderItem {
    let info: ContainerJSON
    init(info: ContainerJSON) { self.info = info }

    var itemIdentifier: NSFileProviderItemIdentifier { containerIdentifier(info.id) }
    var parentItemIdentifier: NSFileProviderItemIdentifier { .rootContainer }
    var filename: String { info.name }
    var contentType: UTType { .folder }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsContentEnumerating] }
    var fileSystemFlags: NSFileProviderFileSystemFlags { [.userReadable, .userWritable, .userExecutable] }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

final class DirectoryItem: NSObject, NSFileProviderItem {
    let containerID: String
    let path: String
    let dirName: String
    init(containerID: String, path: String, name: String) {
        self.containerID = containerID
        self.path = path
        self.dirName = name
    }

    var itemIdentifier: NSFileProviderItemIdentifier { dirIdentifier(containerID, path: path) }
    var parentItemIdentifier: NSFileProviderItemIdentifier {
        let parent = (path as NSString).deletingLastPathComponent
        if parent.isEmpty || parent == "." {
            return containerIdentifier(containerID)
        }
        return dirIdentifier(containerID, path: parent)
    }
    var filename: String { dirName }
    var contentType: UTType { .folder }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsContentEnumerating] }
    var fileSystemFlags: NSFileProviderFileSystemFlags { [.userReadable, .userWritable, .userExecutable] }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

final class ObjectItem: NSObject, NSFileProviderItem {
    let containerID: String
    let entry: DirEntryJSON

    init(containerID: String, entry: DirEntryJSON) {
        self.containerID = containerID
        self.entry = entry
    }

    var itemIdentifier: NSFileProviderItemIdentifier {
        if entry.isDirectory {
            return dirIdentifier(containerID, path: entry.name)
        }
        return objIdentifier(containerID, oid: entry.objectID ?? entry.name)
    }

    var parentItemIdentifier: NSFileProviderItemIdentifier {
        containerIdentifier(containerID)
    }

    var filename: String { entry.name }

    var contentType: UTType {
        entry.isDirectory ? .folder : (UTType(filenameExtension: (entry.name as NSString).pathExtension) ?? .data)
    }

    var documentSize: NSNumber? { entry.isDirectory ? nil : NSNumber(value: entry.size) }
    var capabilities: NSFileProviderItemCapabilities {
        entry.isDirectory ? [.allowsReading, .allowsContentEnumerating] : [.allowsReading, .allowsWriting, .allowsReparenting, .allowsDeleting, .allowsTrashing]
    }
    var fileSystemFlags: NSFileProviderFileSystemFlags {
        // Without .userWritable Finder/CLI will refuse operations like delete/trash
        // before the extension ever sees them.
        entry.isDirectory ? [.userReadable, .userWritable, .userExecutable] : [.userReadable, .userWritable]
    }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

final class SimpleObjectItem: NSObject, NSFileProviderItem {
    let containerID: String
    let objectID: String
    init(containerID: String, objectID: String) {
        self.containerID = containerID
        self.objectID = objectID
    }

    var itemIdentifier: NSFileProviderItemIdentifier { objIdentifier(containerID, oid: objectID) }
    var parentItemIdentifier: NSFileProviderItemIdentifier { containerIdentifier(containerID) }
    var filename: String { objectID }
    var contentType: UTType { .data }
    var capabilities: NSFileProviderItemCapabilities { [.allowsReading, .allowsWriting, .allowsReparenting, .allowsDeleting, .allowsTrashing] }
    var fileSystemFlags: NSFileProviderFileSystemFlags { [.userReadable, .userWritable] }
    var itemVersion: NSFileProviderItemVersion { FileProviderExtension_itemVersion }
}

// MARK: - Enumerators

final class EmptyEnumerator: NSObject, NSFileProviderEnumerator {
    func invalidate() {}
    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        observer.finishEnumerating(upTo: nil)
    }
    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(NSFileProviderSyncAnchor("empty".data(using: .utf8)!))
    }
    func enumerateChanges(for observer: NSFileProviderChangeObserver, from syncAnchor: NSFileProviderSyncAnchor) {
        observer.finishEnumeratingChanges(upTo: NSFileProviderSyncAnchor("empty".data(using: .utf8)!), moreComing: false)
    }
}

final class WorkingSetEnumerator: NSObject, NSFileProviderEnumerator {
    private let anchor: NSFileProviderSyncAnchor

    override init() {
        self.anchor = NSFileProviderSyncAnchor(
            ISO8601DateFormatter().string(from: Date()).data(using: .utf8)!
        )
        super.init()
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        logger.info("WorkingSetEnumerator.enumerateItems (empty, no local DB)")
        observer.finishEnumerating(upTo: nil)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(anchor)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from syncAnchor: NSFileProviderSyncAnchor) {
        logger.info("WorkingSetEnumerator.enumerateChanges (no-op)")
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }
}

final class ContainerEnumerator: NSObject, NSFileProviderEnumerator {
    private let anchor: NSFileProviderSyncAnchor

    override init() {
        self.anchor = NSFileProviderSyncAnchor(
            ISO8601DateFormatter().string(from: Date()).data(using: .utf8)!
        )
        super.init()
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        logger.info("ContainerEnumerator.enumerateItems")
        let containers = listContainersFromBridge()
        logger.info("ContainerEnumerator: \(containers.count, privacy: .public) containers")
        let items: [NSFileProviderItem] = containers.map { ContainerItem(info: $0) }
        observer.didEnumerate(items)
        observer.finishEnumerating(upTo: nil)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(anchor)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from syncAnchor: NSFileProviderSyncAnchor) {
        logger.info("ContainerEnumerator.enumerateChanges")
        let containers = listContainersFromBridge()
        let items: [NSFileProviderItem] = containers.map { ContainerItem(info: $0) }
        observer.didUpdate(items)
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }
}

final class ObjectEnumerator: NSObject, NSFileProviderEnumerator {
    let containerID: String
    let prefix: String
    private let anchor: NSFileProviderSyncAnchor

    init(containerID: String, prefix: String) {
        self.containerID = containerID
        self.prefix = prefix
        self.anchor = NSFileProviderSyncAnchor(
            ISO8601DateFormatter().string(from: Date()).data(using: .utf8)!
        )
        super.init()
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        logger.info("ObjectEnumerator.enumerateItems container=\(self.containerID, privacy: .public) prefix=\(self.prefix, privacy: .public)")
        let items = buildObjectItems()
        logger.info("ObjectEnumerator: \(items.count, privacy: .public) entries")
        observer.didEnumerate(items)
        observer.finishEnumerating(upTo: nil)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(anchor)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from syncAnchor: NSFileProviderSyncAnchor) {
        logger.info("ObjectEnumerator.enumerateChanges container=\(self.containerID, privacy: .public)")
        let items = buildObjectItems()
        observer.didUpdate(items)
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }

    private func buildObjectItems() -> [NSFileProviderItem] {
        let entries = listEntriesFromBridge(containerID: containerID, prefix: prefix)
        var items: [NSFileProviderItem] = []
        for entry in entries {
            if entry.isDirectory {
                let dirPath = prefix.isEmpty ? entry.name : "\(prefix)/\(entry.name)"
                items.append(DirectoryItem(containerID: containerID, path: dirPath, name: entry.name))
            } else {
                items.append(ObjectItem(containerID: containerID, entry: entry))
            }
        }
        return items
    }
}
