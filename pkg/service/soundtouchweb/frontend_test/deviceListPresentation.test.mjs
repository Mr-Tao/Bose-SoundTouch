import assert from 'node:assert/strict';
import test from 'node:test';

import {
    deviceAddress,
    sortDeviceEntries,
} from '../static/js/deviceListPresentation.mjs';

test('sorts hostname-keyed controls by their resolved numeric addresses', () => {
    const entries = [
        ['speaker-ten.local', { info: { name: 'Ten', ip_address: '192.0.2.10' } }],
        ['speaker-two.local', { info: { name: 'Two', ip_address: '192.0.2.2' } }],
    ];

    assert.deepEqual(
        sortDeviceEntries(entries, 'ip').map(([controlID]) => controlID),
        ['speaker-two.local', 'speaker-ten.local'],
    );
    assert.equal(deviceAddress(entries[0][0], entries[0][1]), '192.0.2.10');
});

test('preserves numeric sorting for literal-IP control keys', () => {
    const entries = [
        ['192.0.2.10', { info: { name: 'Ten' } }],
        ['192.0.2.2', { info: { name: 'Two' } }],
    ];

    assert.deepEqual(
        sortDeviceEntries(entries, 'ip').map(([controlID]) => controlID),
        ['192.0.2.2', '192.0.2.10'],
    );
    assert.equal(deviceAddress(entries[0][0], entries[0][1]), '192.0.2.10');
});
