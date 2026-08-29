import assert from 'node:assert/strict';
import test from 'node:test';

import {
    connectivityLabel,
    connectivityState,
    currentZoneMember,
    deviceAddress,
    resolvedZoneMember,
    sortDeviceEntries,
    zoneMemberControlID,
} from '../static/js/devicePresentation.mjs';

test('uses reported connectivity before the legacy connected fallback', () => {
    assert.equal(connectivityState({ status: { connectivity: 'online', isConnected: false } }), 'online');
    assert.equal(connectivityState({ status: { connectivity: 'stale', isConnected: false } }), 'stale');
    assert.equal(connectivityState({ status: { connectivity: 'offline', isConnected: true } }), 'offline');
    assert.equal(connectivityState({ status: { isConnected: true } }), 'online');
    assert.equal(connectivityState(undefined), 'offline');
    assert.equal(connectivityLabel({ status: { connectivity: 'stale' } }), 'Stale');
});

test('sorts hostname controls by resolved address and omits no identity', () => {
    const entries = [
        ['speaker-ten.local', { info: { name: 'Ten', ip_address: '192.0.2.10' } }],
        ['speaker-two.local', { info: { name: 'Two', ip_address: '192.0.2.2' } }],
    ];

    assert.deepEqual(
        sortDeviceEntries(entries, 'ip').map(([controlID]) => controlID),
        ['speaker-two.local', 'speaker-ten.local'],
    );
    assert.equal(deviceAddress(entries[0][0], entries[0][1]), '192.0.2.10');
    assert.deepEqual(
        sortDeviceEntries(entries, 'name').map(([controlID]) => controlID),
        ['speaker-ten.local', 'speaker-two.local'],
    );
});

test('resolves stale zone-detail identities from the current projection', () => {
    const initial = {
        controlId: '192.0.2.20',
        name: 'Living left',
        deviceIds: ['left-id', 'right-id'],
    };
    const current = {
        controlId: 'living.local',
        name: 'Living',
        deviceIds: ['left-id', 'right-id'],
        connectivity: 'online',
    };

    assert.equal(currentZoneMember({ members: [current] }, initial), current);
    assert.deepEqual(resolvedZoneMember({ members: [current] }, initial), {
        member: current,
        controlId: 'living.local',
        name: 'Living',
    });
    assert.equal(zoneMemberControlID({ ip: '192.0.2.99' }), '192.0.2.99');
});
