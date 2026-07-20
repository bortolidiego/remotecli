import Foundation
import ScreenCaptureKit
import CoreMedia
import VideoToolbox
import CoreVideo

/// Captura real com ScreenCaptureKit e codificação H.264 via VideoToolbox.
@available(macOS 13.0, *)
public final class ScreenCaptureKitSource: NSObject, ScreenCaptureSource {
    private var stream: SCStream?
    private var compressionSession: VTCompressionSession?
    private weak var output: ScreenCaptureOutput?
    public private(set) var isRunning: Bool = false

    private var targetSize: CGSize = CGSize(width: 1280, height: 720)
    private var captureSize: CGSize = .zero

    public override init() {
        super.init()
    }

    public func start(target: CaptureTarget, fps: Int, output: ScreenCaptureOutput) async throws {
        guard !isRunning else { return }
        self.output = output

        let filter = try await makeContentFilter(target: target)
        let config = SCStreamConfiguration()
        config.width = Int(targetSize.width)
        config.height = Int(targetSize.height)
        config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
        config.queueDepth = 3
        config.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange

        let stream = SCStream(filter: filter, configuration: config, delegate: self)
        self.stream = stream
        try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "relay.capture"))
        try await stream.startCapture()
        isRunning = true
    }

    public func stop() async {
        isRunning = false
        if let session = compressionSession {
            VTCompressionSessionCompleteFrames(session, untilPresentationTimeStamp: .invalid)
            compressionSession = nil
        }
        try? await stream?.stopCapture()
        stream = nil
    }

    private func setupCompressionSession(size: CGSize) {
        guard compressionSession == nil || captureSize != size else { return }
        captureSize = size
        if let session = compressionSession {
            VTCompressionSessionInvalidate(session)
        }

        let properties: [String: Any] = [
            kVTCompressionPropertyKey_ProfileLevel as String: kVTProfileLevel_H264_Baseline_AutoLevel,
            kVTCompressionPropertyKey_RealTime as String: true,
            kVTCompressionPropertyKey_AllowFrameReordering as String: false,
            kVTCompressionPropertyKey_AverageBitRate as String: 2_000_000,
            kVTCompressionPropertyKey_MaxKeyFrameInterval as String: 60,
        ]
        var session: VTCompressionSession?
        let status = VTCompressionSessionCreate(
            allocator: nil,
            width: Int32(size.width),
            height: Int32(size.height),
            codecType: kCMVideoCodecType_H264,
            encoderSpecification: nil,
            imageBufferAttributes: nil,
            compressedDataAllocator: nil,
            outputCallback: outputCallback,
            refcon: Unmanaged.passUnretained(self).toOpaque(),
            compressionSessionOut: &session
        )
        guard status == noErr, let s = session else {
            output?.didFail(with: CaptureError.compressionSessionCreationFailed)
            return
        }
        compressionSession = s
        for (key, value) in properties {
            VTSessionSetProperty(s, key: key as CFString, value: value as CFTypeRef)
        }
        VTCompressionSessionPrepareToEncodeFrames(s)
    }

    private let outputCallback: VTCompressionOutputCallback = { refcon, _, status, _, sampleBuffer in
        guard status == noErr, let buffer = sampleBuffer else { return }
        let capture = Unmanaged<ScreenCaptureKitSource>.fromOpaque(refcon!).takeUnretainedValue()
        guard let dataBuffer = CMSampleBufferGetDataBuffer(buffer) else { return }
        var length: Int = 0
        var dataPointer: UnsafeMutablePointer<Int8>?
        CMBlockBufferGetDataPointer(dataBuffer, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &length, dataPointerOut: &dataPointer)
        if let pointer = dataPointer, length > 0 {
            let data = Data(bytes: pointer, count: length)
            let pts = CMSampleBufferGetPresentationTimeStamp(buffer)
            capture.output?.didEncodeH264(data, presentationTime: pts)
        }
    }

    private func encodeFrame(_ pixelBuffer: CVPixelBuffer, pts: CMTime) {
        let size = CGSize(width: CVPixelBufferGetWidth(pixelBuffer), height: CVPixelBufferGetHeight(pixelBuffer))
        let scaled = scaleSize(size)
        setupCompressionSession(size: scaled)
        guard let session = compressionSession else { return }
        VTCompressionSessionEncodeFrame(
            session,
            imageBuffer: pixelBuffer,
            presentationTimeStamp: pts,
            duration: .invalid,
            frameProperties: nil,
            sourceFrameRefcon: nil,
            infoFlagsOut: nil
        )
    }

    private func scaleSize(_ size: CGSize) -> CGSize {
        let maxDim: CGFloat = 1920
        let scale = min(maxDim / max(size.width, size.height), 1.0)
        let w = CGFloat(Int(size.width * scale / 2)) * 2
        let h = CGFloat(Int(size.height * scale / 2)) * 2
        return CGSize(width: max(w, 64), height: max(h, 64))
    }
}

@available(macOS 13.0, *)
extension ScreenCaptureKitSource: SCStreamOutput {
    public func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of outputType: SCStreamOutputType) {
        guard outputType == .screen, let pixelBuffer = sampleBuffer.imageBuffer else { return }
        let pts = CMSampleBufferGetPresentationTimeStamp(sampleBuffer)
        encodeFrame(pixelBuffer, pts: pts)
    }
}

@available(macOS 13.0, *)
extension ScreenCaptureKitSource: SCStreamDelegate {
    public func stream(_ stream: SCStream, didStopWithError error: Error) {
        isRunning = false
        output?.didFail(with: error)
    }
}
