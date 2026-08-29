import assert from 'node:assert/strict';
import test from 'node:test';

import { maxReadbackActual, partialFailureMessage } from '../static/js/zoneVolumeResult.mjs';

test('keeps successful readback and names partial failures concisely', () => {
    const data = {
        partial: true,
        members: [
            { name: 'Kitchen', actual: 50 },
            { name: 'Living room', actual: 80, error: 'set volume: timeout' },
        ],
    };

    assert.equal(maxReadbackActual(data), 80);
    assert.equal(partialFailureMessage(data), '1 member failed: Living room');
});

test('caps long failure lists at two names while retaining the count', () => {
    assert.equal(partialFailureMessage({
        members: [
            { name: 'Kitchen', error: 'failed' },
            { name: 'Hall', error: 'failed' },
            { name: 'Office', error: 'failed' },
        ],
    }), '3 members failed: Kitchen, Hall +1');
});
