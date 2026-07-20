import Foundation
import CoreGraphics

#if canImport(AppKit)
import AppKit
#endif

/// Evento de input normalizado [0,1] enviado pela PWA.
public struct InputEvent: Codable {
    public let type: String
    public let x: Double?
    public let y: Double?
    public let button: String?
    public let key: String?
    public let modifiers: [String]?
    public let deltaX: Double?
    public let deltaY: Double?

    public init(type: String, x: Double? = nil, y: Double? = nil, button: String? = nil, key: String? = nil, modifiers: [String]? = nil, deltaX: Double? = nil, deltaY: Double? = nil) {
        self.type = type
        self.x = x
        self.y = y
        self.button = button
        self.key = key
        self.modifiers = modifiers
        self.deltaX = deltaX
        self.deltaY = deltaY
    }
}

/// Protocolo para injeção de input nativo.
public protocol InputInjectorProtocol: AnyObject {
    func apply(event: InputEvent, geometry: CaptureGeometry)
    func setClipboard(text: String)
    func readClipboard() -> String
}

public protocol ClipboardProtocol: AnyObject {
    func write(_ text: String)
    func read() -> String
}

/// Implementação real via CoreGraphics e NSPasteboard.
public final class SystemInputInjector: InputInjectorProtocol {
    private let clipboard: ClipboardProtocol
    private var lastGeometry: CaptureGeometry?

    public init(clipboard: ClipboardProtocol = SystemClipboard()) {
        self.clipboard = clipboard
    }

    public func apply(event: InputEvent, geometry: CaptureGeometry) {
        lastGeometry = geometry
        let point = normalize(point: CGPoint(x: event.x ?? 0, y: event.y ?? 0), geometry: geometry)
        switch event.type {
        case "mouseMove":
            CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left)?.post(tap: .cghidEventTap)
        case "mouseDown":
            let button = mouseButton(from: event.button)
            CGEvent(mouseEventSource: nil, mouseType: button.down, mouseCursorPosition: point, mouseButton: button.button)?.post(tap: .cghidEventTap)
        case "mouseUp":
            let button = mouseButton(from: event.button)
            CGEvent(mouseEventSource: nil, mouseType: button.up, mouseCursorPosition: point, mouseButton: button.button)?.post(tap: .cghidEventTap)
        case "scroll":
            let dy = Int32((event.deltaY ?? 0) * 10)
            let dx = Int32((event.deltaX ?? 0) * 10)
            CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: dy, wheel2: dx, wheel3: 0)?.post(tap: .cghidEventTap)
        case "keyDown":
            if let key = event.key, let code = keyCode(for: key) {
                CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: true)?.post(tap: .cghidEventTap)
            }
        case "keyUp":
            if let key = event.key, let code = keyCode(for: key) {
                CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: false)?.post(tap: .cghidEventTap)
            }
        default:
            break
        }
    }

    public func setClipboard(text: String) {
        clipboard.write(text)
    }

    public func readClipboard() -> String {
        clipboard.read()
    }

    private func normalize(point: CGPoint, geometry: CaptureGeometry) -> CGPoint {
        let x = max(0, min(1, point.x)) * geometry.capture.width
        let y = max(0, min(1, point.y)) * geometry.capture.height
        return CGPoint(x: x, y: y)
    }
}

public final class SystemClipboard: ClipboardProtocol {
    public init() {}

    public func write(_ text: String) {
        #if canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }

    public func read() -> String {
        #if canImport(AppKit)
        return NSPasteboard.general.string(forType: .string) ?? ""
        #else
        return ""
        #endif
    }
}

private struct MouseButtonMapping {
    let button: CGMouseButton
    let down: CGEventType
    let up: CGEventType
}

private func mouseButton(from: String?) -> MouseButtonMapping {
    switch from {
    case "right": return MouseButtonMapping(button: .right, down: .rightMouseDown, up: .rightMouseUp)
    case "middle": return MouseButtonMapping(button: .center, down: .otherMouseDown, up: .otherMouseUp)
    default: return MouseButtonMapping(button: .left, down: .leftMouseDown, up: .leftMouseUp)
    }
}

private func modifierFlags(from: [String]?) -> CGEventFlags {
    var flags: CGEventFlags = []
    for m in from ?? [] {
        switch m {
        case "shift": flags.insert(.maskShift)
        case "control": flags.insert(.maskControl)
        case "alt": flags.insert(.maskAlternate)
        case "meta": flags.insert(.maskCommand)
        default: break
        }
    }
    return flags
}

private func keyCode(for key: String) -> CGKeyCode? {
    let map: [String: CGKeyCode] = [
        "return": 36,
        "tab": 48,
        "space": 49,
        "escape": 53,
        "backspace": 51,
        "arrowUp": 126,
        "arrowDown": 125,
        "arrowLeft": 123,
        "arrowRight": 124,
    ]
    if let code = map[key] { return code }
    if key.count == 1, let scalar = key.unicodeScalars.first {
        let c = String(Character(scalar)).uppercased().first!
        let letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
        if let idx = letters.firstIndex(of: c) {
            let table: [CGKeyCode] = [0,11,8,2,14,3,5,4,34,38,40,37,46,45,31,35,12,15,1,17,32,9,13,7,16,6]
            let offset = letters.distance(from: letters.startIndex, to: idx)
            return table[offset]
        }
    }
    return nil
}
