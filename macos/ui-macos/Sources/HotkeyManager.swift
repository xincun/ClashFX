import Carbon
import Foundation

final class HotkeyManager {
    private static weak var activeInstance: HotkeyManager?

    private var eventHandlerRef: EventHandlerRef?
    private var hotKeyRefs: [UInt32: EventHotKeyRef] = [:]
    private var callbacks: [UInt32: () -> Void] = [:]
    private var lastOpenChord = ""
    private var lastToggleChord = ""
    private var lastModeChord = ""
    private var lastOnOpen: (() -> Void)?
    private var lastOnToggle: (() -> Void)?
    private var lastOnMode: (() -> Void)?
    private var hasAppliedOnce = false
    private var isSuspended = false

    init() {
        HotkeyManager.activeInstance = self
        installCarbonHandler()
    }

    deinit {
        unregisterAll()
        if let handler = eventHandlerRef {
            RemoveEventHandler(handler)
            eventHandlerRef = nil
        }
        if HotkeyManager.activeInstance === self {
            HotkeyManager.activeInstance = nil
        }
    }

    func applyHotkeys(
        openMainWindow: String,
        toggleSystemProxy: String,
        switchOutboundMode: String,
        onOpenMainWindow: @escaping () -> Void,
        onToggleSystemProxy: @escaping () -> Void,
        onSwitchOutboundMode: @escaping () -> Void
    ) -> [String] {
        let normOpen = openMainWindow.trimmingCharacters(in: .whitespacesAndNewlines)
        let normToggle = toggleSystemProxy.trimmingCharacters(in: .whitespacesAndNewlines)
        let normMode = switchOutboundMode.trimmingCharacters(in: .whitespacesAndNewlines)

        lastOnOpen = onOpenMainWindow
        lastOnToggle = onToggleSystemProxy
        lastOnMode = onSwitchOutboundMode

        if hasAppliedOnce && normOpen == lastOpenChord && normToggle == lastToggleChord && normMode == lastModeChord {
            return []
        }
        hasAppliedOnce = true
        lastOpenChord = normOpen
        lastToggleChord = normToggle
        lastModeChord = normMode

        if isSuspended {
            unregisterAll()
            return []
        }
        return registerConfiguredHotkeys()
    }

    func setSuspended(_ suspended: Bool) {
        guard suspended != isSuspended else { return }
        isSuspended = suspended
        if suspended {
            unregisterAll()
        } else if hasAppliedOnce {
            _ = registerConfiguredHotkeys()
        }
    }

    private func registerConfiguredHotkeys() -> [String] {
        unregisterAll()
        var failed: [String] = []
        let slots: [(id: UInt32, chord: String, action: (() -> Void)?)] = [
            (1, lastOpenChord, lastOnOpen),
            (2, lastToggleChord, lastOnToggle),
            (3, lastModeChord, lastOnMode),
        ]
        for slot in slots where !slot.chord.isEmpty {
            guard let action = slot.action else { continue }
            if !register(id: slot.id, chord: slot.chord, action: action) {
                failed.append(slot.chord)
            }
        }
        return failed
    }

    private func unregisterAll() {
        for (_, ref) in hotKeyRefs {
            UnregisterEventHotKey(ref)
        }
        hotKeyRefs.removeAll()
        callbacks.removeAll()
    }

    private func register(id: UInt32, chord: String, action: @escaping () -> Void) -> Bool {
        guard let parsed = parseChord(chord) else {
            return false
        }
        var ref: EventHotKeyRef?
        let hotKeyID = EventHotKeyID(signature: fourCharCode("CFXH"), id: id)
        let status = RegisterEventHotKey(
            parsed.keyCode,
            parsed.modifiers,
            hotKeyID,
            GetApplicationEventTarget(),
            0,
            &ref
        )
        guard status == noErr, let hotKeyRef = ref else {
            return false
        }
        hotKeyRefs[id] = hotKeyRef
        callbacks[id] = action
        return true
    }

    private func installCarbonHandler() {
        var eventSpec = EventTypeSpec(
            eventClass: OSType(kEventClassKeyboard),
            eventKind: UInt32(kEventHotKeyPressed)
        )
        let proc: EventHandlerUPP = { _, event, _ -> OSStatus in
            guard let event else { return OSStatus(eventNotHandledErr) }
            var hkID = EventHotKeyID()
            let err = GetEventParameter(
                event,
                EventParamName(kEventParamDirectObject),
                EventParamType(typeEventHotKeyID),
                nil,
                MemoryLayout<EventHotKeyID>.size,
                nil,
                &hkID
            )
            guard err == noErr else { return err }
            DispatchQueue.main.async {
                HotkeyManager.activeInstance?.callbacks[hkID.id]?()
            }
            return noErr
        }
        InstallEventHandler(
            GetApplicationEventTarget(),
            proc,
            1,
            &eventSpec,
            nil,
            &eventHandlerRef
        )
    }

    func parseChord(_ raw: String) -> (keyCode: UInt32, modifiers: UInt32)? {
        var source = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        var forcedPlusKey = false
        if source.hasSuffix("++") {
            forcedPlusKey = true
            source = String(source.dropLast(1))
        }
        let parts = source
            .split(separator: "+")
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() }
            .filter { !$0.isEmpty }
        guard !parts.isEmpty else { return nil }

        var mods: UInt32 = 0
        var hasFn = false
        var keyName = forcedPlusKey ? "=" : ""

        for part in parts {
            switch part {
            case "cmd", "command", "super", "meta", "win":
                mods |= UInt32(cmdKey)
            case "ctrl", "control":
                mods |= UInt32(controlKey)
            case "alt", "option", "opt":
                mods |= UInt32(optionKey)
            case "shift":
                mods |= UInt32(shiftKey)
            case "fn", "function":
                hasFn = true
            default:
                keyName = part
            }
        }
        guard !keyName.isEmpty, let code = keyCodeForName(keyName) else {
            return nil
        }
        // macOS synthesizes kEventKeyModifierFnMask on F1..F20 and treats Fn+F12 and F12
        // as the same Carbon hotkey slot (mods=0). Keep explicit non-Fn modifiers if provided.
        if !isFunctionKeyName(keyName) {
            if hasFn {
                mods |= UInt32(kEventKeyModifierFnMask)
            }
            guard mods != 0 else {
                return nil
            }
        }
        return (code, mods)
    }

    private func isFunctionKeyName(_ key: String) -> Bool {
        guard key.hasPrefix("f"), let num = Int(key.dropFirst()) else {
            return false
        }
        return num >= 1 && num <= 20
    }

    private func keyCodeForName(_ key: String) -> UInt32? {
        let map: [String: Int] = [
            "a": kVK_ANSI_A, "b": kVK_ANSI_B, "c": kVK_ANSI_C, "d": kVK_ANSI_D,
            "e": kVK_ANSI_E, "f": kVK_ANSI_F, "g": kVK_ANSI_G, "h": kVK_ANSI_H,
            "i": kVK_ANSI_I, "j": kVK_ANSI_J, "k": kVK_ANSI_K, "l": kVK_ANSI_L,
            "m": kVK_ANSI_M, "n": kVK_ANSI_N, "o": kVK_ANSI_O, "p": kVK_ANSI_P,
            "q": kVK_ANSI_Q, "r": kVK_ANSI_R, "s": kVK_ANSI_S, "t": kVK_ANSI_T,
            "u": kVK_ANSI_U, "v": kVK_ANSI_V, "w": kVK_ANSI_W, "x": kVK_ANSI_X,
            "y": kVK_ANSI_Y, "z": kVK_ANSI_Z,
            "0": kVK_ANSI_0, "1": kVK_ANSI_1, "2": kVK_ANSI_2, "3": kVK_ANSI_3,
            "4": kVK_ANSI_4, "5": kVK_ANSI_5, "6": kVK_ANSI_6, "7": kVK_ANSI_7,
            "8": kVK_ANSI_8, "9": kVK_ANSI_9,
            "f1": kVK_F1, "f2": kVK_F2, "f3": kVK_F3, "f4": kVK_F4,
            "f5": kVK_F5, "f6": kVK_F6, "f7": kVK_F7, "f8": kVK_F8,
            "f9": kVK_F9, "f10": kVK_F10, "f11": kVK_F11, "f12": kVK_F12,
            "f13": kVK_F13, "f14": kVK_F14, "f15": kVK_F15, "f16": kVK_F16,
            "f17": kVK_F17, "f18": kVK_F18, "f19": kVK_F19, "f20": kVK_F20,
            "space": kVK_Space, "return": kVK_Return, "enter": kVK_Return,
            "tab": kVK_Tab, "escape": kVK_Escape, "esc": kVK_Escape,
            "backspace": kVK_Delete, "delete": kVK_ForwardDelete, "del": kVK_ForwardDelete,
            "home": kVK_Home, "end": kVK_End,
            "pageup": kVK_PageUp, "pagedown": kVK_PageDown,
            "arrowup": kVK_UpArrow, "up": kVK_UpArrow,
            "arrowdown": kVK_DownArrow, "down": kVK_DownArrow,
            "arrowleft": kVK_LeftArrow, "left": kVK_LeftArrow,
            "arrowright": kVK_RightArrow, "right": kVK_RightArrow,
            "minus": kVK_ANSI_Minus, "-": kVK_ANSI_Minus,
            "equal": kVK_ANSI_Equal, "=": kVK_ANSI_Equal, "+": kVK_ANSI_Equal,
            "[": kVK_ANSI_LeftBracket, "]": kVK_ANSI_RightBracket,
            ";": kVK_ANSI_Semicolon, "'": kVK_ANSI_Quote,
            ",": kVK_ANSI_Comma, ".": kVK_ANSI_Period, "/": kVK_ANSI_Slash,
            "\\": kVK_ANSI_Backslash, "`": kVK_ANSI_Grave,
        ]
        if let found = map[key] {
            return UInt32(found)
        }
        return nil
    }

    private func fourCharCode(_ str: String) -> FourCharCode {
        var result: FourCharCode = 0
        for byte in str.utf8.prefix(4) {
            result = (result << 8) + FourCharCode(byte)
        }
        return result
    }
}
