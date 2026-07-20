import { describe, expect, it } from 'vitest'
import { MsgType, SecureChannel } from './channel'

describe('SecureChannel', () => {
  async function makeKey() {
    return crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, [
      'encrypt',
      'decrypt',
    ])
  }

  it('encrypts and decrypts', async () => {
    const key = await makeKey()
    const ch = new SecureChannel(key, 'dev', 'sess', 'webrtc')
    const msg = await ch.encrypt(MsgType.Control, new TextEncoder().encode('hello'))
    expect(msg.byteLength).toBeGreaterThan(4 + 21)
    const dec = await ch.decrypt(msg)
    expect(dec.type).toBe(MsgType.Control)
    expect(new TextDecoder().decode(dec.plaintext)).toBe('hello')
    expect(dec.sequence).toBe(1)
  })

  it('rejects replay', async () => {
    const key = await makeKey()
    const ch = new SecureChannel(key, 'dev', 'sess', 'webrtc')
    const msg = await ch.encrypt(MsgType.Clipboard, new TextEncoder().encode('x'))
    await ch.decrypt(msg)
    await expect(ch.decrypt(msg)).rejects.toThrow('replay')
  })

  it('rejects plaintext', async () => {
    const key = await makeKey()
    const ch = new SecureChannel(key, 'dev', 'sess', 'webrtc')
    await expect(ch.decrypt(new TextEncoder().encode('plain text message'))).rejects.toThrow()
  })

  it('rejects wrong AAD channel', async () => {
    const key = await makeKey()
    const ch1 = new SecureChannel(key, 'dev', 'sess', 'webrtc')
    const ch2 = new SecureChannel(key, 'dev', 'sess', 'other')
    const msg = await ch1.encrypt(MsgType.Input, new TextEncoder().encode('a'))
    await expect(ch2.decrypt(msg)).rejects.toThrow()
  })
})
