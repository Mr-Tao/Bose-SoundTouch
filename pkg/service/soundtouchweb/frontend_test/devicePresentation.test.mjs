import assert from 'node:assert/strict';
import test from 'node:test';

import {
    actualVolume,
    connectivityLabel,
    connectivityState,
    currentZoneMember,
    resolvedZoneMember,
    zoneMemberControlID,
    zoneMemberPresentation,
} from '../static/js/devicePresentation.mjs';

test('uses reported tri-state connectivity before the compatibility fallback', () => {
    assert.equal(connectivityState({ status: { connectivity: 'online', isConnected: false } }), 'online');
    assert.equal(connectivityState({ status: { connectivity: 'stale', isConnected: false } }), 'stale');
    assert.equal(connectivityState({ status: { connectivity: 'offline', isConnected: true } }), 'offline');
});

test('falls back to the legacy connected flag and supplies an accessible label', () => {
    assert.equal(connectivityState({ status: { isConnected: true } }), 'online');
    assert.equal(connectivityState({ status: { isConnected: false } }), 'offline');
    assert.equal(connectivityState(undefined), 'offline');
    assert.equal(connectivityLabel({ status: { connectivity: 'stale' } }), 'Stale');
});

test('returns a bounded actual volume only when one is available', () => {
    assert.equal(actualVolume({ status: { volume: { ActualVolume: 14 } } }), 14);
    assert.equal(actualVolume({ status: { volume: { ActualVolume: 101 } } }), 100);
    assert.equal(actualVolume({ status: { volume: { ActualVolume: -1 } } }), 0);
    assert.equal(actualVolume({ status: { volume: { ActualVolume: '12' } } }), 12);
    assert.equal(actualVolume({ status: { volume: { ActualVolume: null } } }), null);
    assert.equal(actualVolume({ status: {} }), null);
});

test('presents collapsed zone-detail members without physical device cards', () => {
    const stale = {
        controlId: '192.0.2.20',
        deviceIds: ['stale-id'],
        connectivity: 'stale',
        actualVolume: 35,
    };
    const offline = {
        controlId: '192.0.2.30',
        deviceIds: ['offline-id'],
        connectivity: 'offline',
        actualVolume: 0,
    };

    assert.deepEqual(zoneMemberPresentation(stale), {
        connectivity: 'stale', label: 'Stale', role: 'status', volume: 35,
    });
    assert.deepEqual(zoneMemberPresentation(offline), {
        connectivity: 'offline', label: 'Offline', role: 'status', volume: 0,
    });

    const currentProjection = { members: [{ ...stale, connectivity: 'offline', actualVolume: 34 }] };
    assert.deepEqual(zoneMemberPresentation(currentZoneMember(currentProjection, stale)), {
        connectivity: 'offline', label: 'Offline', role: 'status', volume: 34,
    });

    const stereoPair = {
        controlId: '192.0.2.40',
        ip: '192.0.2.40',
        deviceIds: ['left-id', 'right-id'],
    };
    assert.equal(zoneMemberControlID(stereoPair), '192.0.2.40');
});

test('resolves zone member display and controls from the current projection', () => {
    const initial = {
        controlId: '192.0.2.20',
        name: 'Living left',
        deviceIds: ['left-id', 'right-id'],
    };
    const current = {
        controlId: '192.0.2.21',
        name: 'Living',
        deviceIds: ['left-id', 'right-id'],
        connectivity: 'online',
    };

    assert.deepEqual(resolvedZoneMember({ members: [current] }, initial), {
        member: current,
        controlId: '192.0.2.21',
        name: 'Living',
    });

    const physicalStereoRepresentative = {
        controlId: 'LEFT',
        name: 'Living Room Left',
        deviceIds: ['LEFT'],
    };
    const logicalStereoMember = {
        controlId: 'LEFT',
        name: 'Living Room',
        deviceIds: ['LEFT', 'RIGHT'],
    };
    assert.deepEqual(resolvedZoneMember(
        { members: [logicalStereoMember] }, physicalStereoRepresentative), {
        member: logicalStereoMember,
        controlId: 'LEFT',
        name: 'Living Room',
    });
    assert.deepEqual(resolvedZoneMember(undefined, {
        ip: '192.0.2.99',
        deviceIds: ['missing-id'],
    }), {
        member: { ip: '192.0.2.99', deviceIds: ['missing-id'] },
        controlId: '192.0.2.99',
        name: '192.0.2.99',
    });
});
