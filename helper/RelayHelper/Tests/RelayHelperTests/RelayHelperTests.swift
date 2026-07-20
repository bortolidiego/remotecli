import Foundation
import XCTest
@testable import RelayHelper

private struct MockCapture: ScreenCaptureProtocol {
    let state: PermissionState
    func preflightScreenRecording() -> PermissionState { state }
}

private struct MockInput: InputControlProtocol {
    let state: PermissionState
    func preflightAccessibility() -> PermissionState { state }
}

private struct MockAgent: RelayAgentProtocol {
    let ok: Bool
    func ping(endpoint: String) async throws -> Bool { ok }
}

final class RelayHelperTests: XCTestCase {
    func testBootstrapSetsEndpoint() {
        let helper = RelayHelper.shared
        helper.agentEndpoint = "http://127.0.0.1:24109"
        helper.bootstrap()
        XCTAssertEqual(helper.agentEndpoint, "http://127.0.0.1:24109")
    }

    func testPingAgentUsesAgentProtocol() async throws {
        let helper = RelayHelper(
            screenCapture: MockCapture(state: .granted),
            inputControl: MockInput(state: .denied),
            agent: MockAgent(ok: true)
        )
        helper.agentEndpoint = "http://127.0.0.1:24109"
        let ok = try await helper.pingAgent()
        XCTAssertTrue(ok)
    }

    func testPermissionChecksAreExposed() {
        let helper = RelayHelper(
            screenCapture: MockCapture(state: .granted),
            inputControl: MockInput(state: .denied),
            agent: MockAgent(ok: false)
        )
        let status = helper.permissionStatus()
        XCTAssertEqual(status.screenRecording, .granted)
        XCTAssertEqual(status.accessibility, .denied)
    }

    func testSystemSettingsLinks() {
        XCTAssertEqual(
            SystemSettingsLink.screenRecording,
            "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
        )
        XCTAssertEqual(
            SystemSettingsLink.accessibility,
            "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
        )
    }

    func testPingAgentFailsWithoutServer() async {
        let helper = RelayHelper(
            screenCapture: MockCapture(state: .unavailable),
            inputControl: MockInput(state: .unavailable),
            agent: HTTPRelayAgent()
        )
        helper.agentEndpoint = "http://127.0.0.1:1"
        let ok = try? await helper.pingAgent()
        XCTAssertFalse(ok ?? true)
    }
}
