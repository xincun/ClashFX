//
//  AutoUpgradeManager.swift
//  ClashFX personal build
//

import Cocoa

/// This personal fork is updated by rebuilding from source. Do not initialize
/// Sparkle: an official update would replace its local features.
final class AutoUpgradeManager: NSObject {
    static let shared = AutoUpgradeManager()
    static let labChannelIdentifier = "lab"
    static var isLabBuild: Bool { false }
    static var currentChannelDisplayName: String {
        NSLocalizedString("Local build — updates disabled", comment: "")
    }
    static var shouldShowLabBadge: Bool { false }

    func setup() {}

    func setupCheckForUpdatesMenuItem(_ item: NSMenuItem) {
        item.title = NSLocalizedString("Automatic updates blocked in this local build", comment: "")
        item.target = nil
        item.action = nil
        item.isEnabled = false
    }

    func addChannelMenuItem(_ button: NSPopUpButton) {
        button.removeAllItems()
        button.addItem(withTitle: Self.currentChannelDisplayName)
        button.target = nil
        button.action = nil
        button.isEnabled = false
    }
}
