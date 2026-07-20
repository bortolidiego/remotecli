import Foundation
import CryptoKit

/// Protocolo binário Go↔helper.
/// Frame: [4 bytes length big-endian][1 byte type][payload]
public enum IPCMessageType: UInt8 {
    case h264 = 0x01
    case geometry = 0x02
    case input = 0x03
    case clipboard = 0x04
    case ping = 0x05
    case pong = 0x06
    case auth = 0x07
}

public struct IPCFrame {
    public let type: IPCMessageType
    public let payload: Data

    public init(type: IPCMessageType, payload: Data) {
        self.type = type
        self.payload = payload
    }

    /// Codifica o frame para transmissão.
    public func encode() -> Data {
        var data = Data()
        let length = UInt32(1 + payload.count)
        data.append(contentsOf: length.bigEndian.bytes)
        data.append(type.rawValue)
        data.append(payload)
        return data
    }

    /// Lê um frame de um InputStream (bloqueante).
    public static func read(from stream: InputStream) -> IPCFrame? {
        guard let lengthData = readExactly(stream, 4), lengthData.count == 4 else { return nil }
        let length = UInt32(bigEndian: lengthData.withUnsafeBytes { $0.load(as: UInt32.self) })
        guard length > 0, length <= 64 * 1024 * 1024 else { return nil }
        guard let body = readExactly(stream, Int(length)), body.count == Int(length) else { return nil }
        guard let type = IPCMessageType(rawValue: body[0]) else { return nil }
        let payload = body.subdata(in: 1..<body.count)
        return IPCFrame(type: type, payload: payload)
    }

    private static func readExactly(_ stream: InputStream, _ count: Int) -> Data? {
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

public extension UInt32 {
    var bytes: [UInt8] {
        withUnsafeBytes(of: self.bigEndian) { Array($0) }
    }
}

/// Handshake: servidor envia nonce (16 bytes); cliente responde com HMAC-SHA256(secret, nonce).
public func ipcAuthResponse(secret: Data, nonce: Data) -> Data {
    var mac = HMAC<SHA256>.authenticationCode(for: nonce, using: SymmetricKey(data: secret))
    return Data(mac)
}
