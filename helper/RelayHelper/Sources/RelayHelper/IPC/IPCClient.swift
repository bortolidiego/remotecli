import Foundation

/// Cliente IPC que se conecta ao socket Unix do agente Go.
public protocol IPCClientProtocol: AnyObject {
    var isConnected: Bool { get }
    func connect(path: String, secret: Data) async throws
    func disconnect()
    func send(_ frame: IPCFrame) throws
    func sendH264(_ data: Data) throws
    func sendGeometry(_ json: Data) throws
    func onFrame(_ handler: @escaping (IPCFrame) -> Void)
}

public final class IPCClient: NSObject, IPCClientProtocol {
    private var inputStream: InputStream?
    private var outputStream: OutputStream?
    private var frameHandler: ((IPCFrame) -> Void)?
    private let queue = DispatchQueue(label: "relay.ipc")
    private var secret: Data?
    public private(set) var isConnected: Bool = false

    public override init() {
        super.init()
    }

    public func connect(path: String, secret: Data) async throws {
        var readStream: Unmanaged<CFReadStream>?
        var writeStream: Unmanaged<CFWriteStream>?
        CFStreamCreatePairWithSocketToHost(kCFAllocatorDefault, path as CFString, 0, &readStream, &writeStream)
        guard let cfInput = readStream?.takeRetainedValue(), let cfOutput = writeStream?.takeRetainedValue() else {
            throw IPCError.connectionFailed
        }
        let input = cfInput as InputStream
        let output = cfOutput as OutputStream
        self.inputStream = input
        self.outputStream = output
        self.secret = secret
        input.delegate = self
        output.delegate = self
        input.schedule(in: .main, forMode: .common)
        output.schedule(in: .main, forMode: .common)
        input.open()
        output.open()

        // Handshake: ler nonce e enviar HMAC.
        guard let nonce = readExactly(input, 16), nonce.count == 16 else {
            throw IPCError.handshakeFailed
        }
        let auth = ipcAuthResponse(secret: secret, nonce: nonce)
        try send(IPCFrame(type: .auth, payload: auth))
        isConnected = true

        // Loop de leitura.
        Task {
            while self.isConnected {
                if let frame = IPCFrame.read(from: input) {
                    await MainActor.run {
                        self.frameHandler?(frame)
                    }
                } else {
                    self.isConnected = false
                    break
                }
            }
        }
    }

    public func disconnect() {
        isConnected = false
        inputStream?.close()
        outputStream?.close()
        inputStream = nil
        outputStream = nil
    }

    public func send(_ frame: IPCFrame) throws {
        guard let output = outputStream, output.hasSpaceAvailable else {
            throw IPCError.notConnected
        }
        let data = frame.encode()
        var remaining = data
        while remaining.count > 0 {
            let written = remaining.withUnsafeBytes { ptr in
                output.write(ptr.bindMemory(to: UInt8.self).baseAddress!, maxLength: remaining.count)
            }
            if written <= 0 { throw IPCError.writeFailed }
            remaining.removeFirst(written)
        }
    }

    public func sendH264(_ data: Data) throws {
        try send(IPCFrame(type: .h264, payload: data))
    }

    public func sendGeometry(_ json: Data) throws {
        try send(IPCFrame(type: .geometry, payload: json))
    }

    public func onFrame(_ handler: @escaping (IPCFrame) -> Void) {
        frameHandler = handler
    }

    private func readExactly(_ stream: InputStream, _ count: Int) -> Data? {
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 1024)
        while data.count < count {
            let remaining = count - data.count
            let read = stream.read(&buffer, maxLength: min(remaining, buffer.count))
            if read <= 0 { return nil }
            data.append(contentsOf: buffer.prefix(read))
        }
        return data
    }
}

extension IPCClient: StreamDelegate {
    public func stream(_ aStream: Stream, handle eventCode: Stream.Event) {
        switch eventCode {
        case .errorOccurred, .endEncountered:
            isConnected = false
        default:
            break
        }
    }
}

public enum IPCError: Error {
    case connectionFailed
    case handshakeFailed
    case notConnected
    case writeFailed
}
