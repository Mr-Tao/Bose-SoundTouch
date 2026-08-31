import assert from 'node:assert/strict';
import test from 'node:test';

import { api } from '../static/js/api.js';

test('logical zone volume methods use only volume routes', async () => {
    const calls = [];
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (url, options) => {
        calls.push({ url, options });
        return { json: async () => ({ success: true }) };
    };

    try {
        await api.zoneVolume('master.local', 42);
        await api.zoneMemberVolume('master.local', 'stereo.local', 37);
    } finally {
        globalThis.fetch = originalFetch;
    }

    assert.deepEqual(calls, [
        {
            url: '/api/control/devices/master.local/zone/volume/42',
            options: { method: 'POST' },
        },
        {
            url: '/api/control/devices/master.local/zone/member/stereo.local/volume/37',
            options: { method: 'POST' },
        },
    ]);
    assert.equal(calls.some(call => call.url.includes('balance')), false);
});
