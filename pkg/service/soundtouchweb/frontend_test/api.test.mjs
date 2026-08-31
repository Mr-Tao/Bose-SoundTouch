import assert from 'node:assert/strict';
import test from 'node:test';

import { api } from '../static/js/api.js';

test('topology reconciliation bypasses the browser HTTP cache', async () => {
    const calls = [];
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (url, options) => {
        calls.push({ url, options });
        return { json: async () => ({ success: true, data: {} }) };
    };

    try {
        await api.devices();
        await api.zone('speaker.local');
    } finally {
        globalThis.fetch = originalFetch;
    }

    assert.deepEqual(calls, [
        { url: '/api/control/devices', options: { cache: 'no-store' } },
        { url: '/api/control/devices/speaker.local/zone', options: { cache: 'no-store' } },
    ]);
});
