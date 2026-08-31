import assert from 'node:assert/strict';
import test from 'node:test';

import {
    maxReadbackActual,
    partialFailureMessage,
} from '../static/js/zoneVolumeResult.mjs';

test('uses every known readback when a member reports a write error', () => {
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

test('falls back to the generic partial message without member errors', () => {
    assert.equal(maxReadbackActual({ members: [{ error: 'readback failed' }] }), null);
    assert.equal(partialFailureMessage({ partial: true, members: [] }),
        'Some group members could not be updated.');
});
