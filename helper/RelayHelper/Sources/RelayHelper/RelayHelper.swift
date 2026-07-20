import Foundation
#if canImport(AppKit)
import AppKit
import ApplicationServices
import CoreGraphics
#endif

public enum PermissionState: Equatable {
    case granted
    case denied
    case unavailable
}

public struct HelperPermissions: Equatable {
    public let screenRecording: PermissionState
    public let accessibility: PermissionState
}

public protocol ScreenCaptureProtocol {
    func preflightScreenRecording() -> PermissionState
}

public protocol InputControlProtocol {
    func preflightAccessibility() -> PermissionState
}

public protocol RelayAgentProtocol {
    func ping(endpoint: String) async throws -> Bool
}

public struct SystemScreenCapture: ScreenCaptureProtocol {
    public init() {}

    public func preflightScreenRecording() -> PermissionState {
        #if canImport(AppKit)
        return CGPreflightScreenCaptureAccess() ? .granted : .denied
        #else
        return .unavailable
        #endif
    }
}

public struct SystemInputControl: InputControlProtocol {
    public init() {}

    public func preflightAccessibility() -> PermissionState {
        #if canImport(AppKit)
        return AXIsProcessTrusted() ? .granted : .denied
        #else
        return .unavailable
        #endif
    }
}

public struct HTTPRelayAgent: RelayAgentProtocol {
    public init() {}

    public func ping(endpoint: String) async throws -> Bool {
        guard let url = URL(string: "\(endpoint)/health") else {
            return false
        }
        let (_, response) = try await URLSession.shared.data(from: url)
        guard let http = response as? HTTPURLResponse else { return false }
        return http.statusCode == 200
    }
}

public enum SystemSettingsLink {
    public static let screenRecording = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
    public static let accessibility = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
}

/// RelayHelper é o launcher/menu-bar LSUIElement para macOS.
public final class RelayHelper: NSObject {
    public static let shared = RelayHelper()
    public var agentEndpoint: String = "http://127.0.0.1:24109"
    public var sessionID: String?

    private let screenCapture: ScreenCaptureProtocol
    private let inputControl: InputControlProtocol
    private let agent: RelayAgentProtocol
    #if canImport(AppKit)
    private var statusItem: NSStatusItem?
    #endif

    public convenience override init() {
        self.init(screenCapture: SystemScreenCapture(), inputControl: SystemInputControl(), agent: HTTPRelayAgent())
    }

    public init(screenCapture: ScreenCaptureProtocol, inputControl: InputControlProtocol, agent: RelayAgentProtocol) {
        self.screenCapture = screenCapture
        self.inputControl = inputControl
        self.agent = agent
        super.init()
    }

    /// Inicializa o launcher como app de menu bar sem janela principal.
    public func bootstrap() {
        configureMenuBar()
        print("RelayHelper iniciado em \(agentEndpoint)")
    }

    public func permissionStatus() -> HelperPermissions {
        HelperPermissions(
            screenRecording: screenCapture.preflightScreenRecording(),
            accessibility: inputControl.preflightAccessibility()
        )
    }

    /// Verifica se o agente local responde.
    public func pingAgent() async throws -> Bool {
        try await agent.ping(endpoint: agentEndpoint)
    }

    /// Abre a PWA no navegador padrão.
    public func openPWA() {
        guard let url = URL(string: agentEndpoint) else { return }
        #if canImport(AppKit)
        NSWorkspace.shared.open(url)
        #else
        print("Abrir \(url)")
        #endif
    }

    private func configureMenuBar() {
        #if canImport(AppKit)
        guard statusItem == nil else { return }
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.button?.title = "R"

        let menu = NSMenu()
        menu.addItem(NSMenuItem(title: "Open Relay", action: #selector(openRelayMenuAction), keyEquivalent: "o"))
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: permissionTitle(), action: nil, keyEquivalent: ""))
        menu.addItem(NSMenuItem(title: "Open Screen Recording Settings", action: #selector(openScreenRecordingSettings), keyEquivalent: "s"))
        menu.addItem(NSMenuItem(title: "Open Accessibility Settings", action: #selector(openAccessibilitySettings), keyEquivalent: "a"))
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: "Quit Relay Helper", action: #selector(quitMenuAction), keyEquivalent: "q"))
        item.menu = menu
        statusItem = item
        #endif
    }

    private func permissionTitle() -> String {
        let permissions = permissionStatus()
        return "Screen: \(permissions.screenRecording) / Accessibility: \(permissions.accessibility)"
    }

    #if canImport(AppKit)
    @objc private func openRelayMenuAction() {
        openPWA()
    }

    @objc private func openScreenRecordingSettings() {
        openSystemSettings(SystemSettingsLink.screenRecording)
    }

    @objc private func openAccessibilitySettings() {
        openSystemSettings(SystemSettingsLink.accessibility)
    }

    private func openSystemSettings(_ rawURL: String) {
        guard let url = URL(string: rawURL) else { return }
        NSWorkspace.shared.open(url)
    }

    @objc private func quitMenuAction() {
        NSApplication.shared.terminate(nil)
    }
    #endif
}

#if canImport(AppKit)
/// NSApplication delegate para execução como LSUIElement/accessory.
public final class RelayAppDelegate: NSObject, NSApplicationDelegate {
    public static let shared = RelayAppDelegate()

    @MainActor
    public func applicationDidFinishLaunching(_ notification: Notification) {
        NSApplication.shared.setActivationPolicy(.accessory)
        RelayHelper.shared.bootstrap()
    }
}
#endif

@main
struct RelayHelperMain {
    static func main() {
        #if canImport(AppKit)
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        let delegate = RelayAppDelegate()
        app.delegate = delegate
        app.run()
        #else
        RelayHelper.shared.bootstrap()
        RunLoop.main.run()
        #endif
    }
}
