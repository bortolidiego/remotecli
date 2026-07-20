import Foundation
import ScreenCaptureKit
import CoreGraphics

/// Resultado de uma captura: H264 NAL access unit ou erro.
public protocol ScreenCaptureOutput: AnyObject {
    func didEncodeH264(_ data: Data, presentationTime: CMTime)
    func didFail(with error: Error)
}

/// Abstração de captura de tela/janela.
public protocol ScreenCaptureSource: AnyObject {
    var isRunning: Bool { get }
    func start(target: CaptureTarget, fps: Int, output: ScreenCaptureOutput) async throws
    func stop() async
}

/// Alvo de captura: display ou janela.
public enum CaptureTarget {
    case display(CGDirectDisplayID)
    case window(CGWindowID)
}

/// Geometria de captura enviada ao agente Go.
public struct CaptureGeometry: Codable, Equatable {
    public var capture: CaptureRect
    public var video: CaptureRect
    public var rotation: Int = 0

    public init(capture: CaptureRect, video: CaptureRect, rotation: Int = 0) {
        self.capture = capture
        self.video = video
        self.rotation = rotation
    }
}

public struct CaptureRect: Codable, Equatable {
    public var x: Double
    public var y: Double
    public var width: Double
    public var height: Double

    public init(x: Double, y: Double, width: Double, height: Double) {
        self.x = x
        self.y = y
        self.width = width
        self.height = height
    }

    public var size: CGSize { CGSize(width: width, height: height) }
}

/// Erros de captura.
public enum CaptureError: Error {
    case noShareableContent
    case targetNotFound
    case compressionSessionCreationFailed
}

/// Busca conteúdo compartilhável e monta filtro.
@available(macOS 12.3, *)
public func makeContentFilter(target: CaptureTarget) async throws -> SCContentFilter {
    let content = try await SCShareableContent.current
    switch target {
    case .display(let id):
        guard let display = content.displays.first(where: { $0.displayID == id }) else {
            throw CaptureError.targetNotFound
        }
        return SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
    case .window(let id):
        guard let window = content.windows.first(where: { $0.windowID == id }) else {
            throw CaptureError.targetNotFound
        }
        return SCContentFilter(desktopIndependentWindow: window)
    }
}
