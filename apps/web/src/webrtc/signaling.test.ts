import { describe, expect, it } from 'vitest'
import { SignalingClient } from './signaling'

describe('SignalingClient construction', () => {
  it('starts in idle state', () => {
    const client = new SignalingClient({
      auth: { deviceId: 'dev', leaseToken: 'lease' },
      sessionId: 'sess',
      sessionKey: null as unknown as CryptoKey,
      deviceId: 'dev',
    })
    expect(client.getState()).toBe('idle')
    expect(client.isDataChannelOpen()).toBe(false)
  })
})
