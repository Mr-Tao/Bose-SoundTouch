import assert from 'node:assert/strict';
import test from 'node:test';
import { api } from '../static/js/api.js';

test('fresh preset reads bypass caches and scoped mutations preserve target headers and frozen content', async () => {
    const originalFetch = globalThis.fetch;
    const requests = [];
    globalThis.fetch = async (url, options) => {
        requests.push({ url, options });
        return { json: async () => ({ success: true }) };
    };
    try {
        const controller = new AbortController();
        const options = { signal: controller.signal, headers: { 'X-AfterTouch-Preset-Target': 'physical' } };
        const content = { source: 'TUNEIN', location: 's1', itemName: 'Frozen station' };
        await api.devicePresets('speaker', options);
        await api.storePresetContent('speaker', 3, content, options);
        await api.removePreset('speaker', 2, options);
        assert.equal(requests[0].url, '/api/control/devices/speaker/presets');
        assert.equal(requests[0].options.cache, 'no-store');
        assert.equal(requests[1].options.method, 'POST');
        assert.equal(requests[1].options.headers['Content-Type'], 'application/json');
        assert.deepEqual(JSON.parse(requests[1].options.body), content);
        assert.equal(requests[2].options.method, 'DELETE');
        for (const request of requests) {
            assert.equal(request.options.headers['X-AfterTouch-Preset-Target'], 'physical');
            assert.equal(request.options.signal, controller.signal);
        }
    } finally { globalThis.fetch = originalFetch; }
});
