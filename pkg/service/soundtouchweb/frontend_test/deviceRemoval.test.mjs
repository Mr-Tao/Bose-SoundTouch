import assert from 'node:assert/strict';
import test from 'node:test';

import { removeDeviceAndRefresh } from '../static/js/deviceRemoval.js';

function operation(overrides = {}) {
    const calls = [];
    return {
        calls,
        options: {
            id: '192.0.2.10',
            name: 'Kitchen',
            remove: async (id) => {
                calls.push(['remove', id]);
                return { success: true };
            },
            refresh: async () => calls.push(['refresh']),
            showDeviceList: () => calls.push(['showDeviceList']),
            notify: (message) => calls.push(['notify', message]),
            ...overrides,
        },
    };
}

test('waits for successful removal before navigating and refreshing', async () => {
    let resolveRemoval;
    const pendingRemoval = new Promise(resolve => { resolveRemoval = resolve; });
    const { calls, options } = operation({
        remove: (id) => {
            calls.push(['remove', id]);
            return pendingRemoval;
        },
    });

    const result = removeDeviceAndRefresh(options);
    await Promise.resolve();
    assert.deepEqual(calls, [['remove', '192.0.2.10']]);

    resolveRemoval({ success: true });
    assert.equal(await result, true);
    assert.deepEqual(calls, [
        ['remove', '192.0.2.10'],
        ['showDeviceList'],
        ['refresh'],
        ['notify', 'Removed "Kitchen"'],
    ]);
});

test('keeps the current view and state when removal is rejected', async () => {
    const { calls, options } = operation({
        remove: async (id) => {
            calls.push(['remove', id]);
            return { success: false, error: 'Device is busy' };
        },
    });

    assert.equal(await removeDeviceAndRefresh(options), false);
    assert.deepEqual(calls, [
        ['remove', '192.0.2.10'],
        ['notify', 'Device is busy'],
    ]);
});

test('reports transport failures without navigating or refreshing', async () => {
    const { calls, options } = operation({
        remove: async (id) => {
            calls.push(['remove', id]);
            throw new Error('network failure');
        },
    });

    assert.equal(await removeDeviceAndRefresh(options), false);
    assert.deepEqual(calls, [
        ['remove', '192.0.2.10'],
        ['notify', 'Failed to remove device'],
    ]);
});

test('retains successful removal when the authoritative refresh fails', async () => {
    const { calls, options } = operation({
        refresh: async () => {
            calls.push(['refresh']);
            throw new Error('refresh failure');
        },
    });

    assert.equal(await removeDeviceAndRefresh(options), true);
    assert.deepEqual(calls, [
        ['remove', '192.0.2.10'],
        ['showDeviceList'],
        ['refresh'],
        ['notify', 'Removed "Kitchen", but failed to refresh devices'],
    ]);
});
