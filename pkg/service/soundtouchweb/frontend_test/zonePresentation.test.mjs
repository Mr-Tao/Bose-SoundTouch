import assert from 'node:assert/strict';
import test from 'node:test';

import {
    physicalMemberMetadata,
    zoneCardPresentation,
    zoneMemberCountSummary,
    zoneMemberMetadata,
} from '../static/js/zonePresentation.mjs';

test('healthy zone cards show one health state and only the logical count', () => {
    assert.deepEqual(zoneCardPresentation({
        memberCount: 2,
        physicalMemberCount: 3,
        availableMemberCount: 2,
        degraded: false,
        members: [
            { physicalMembers: [{ available: true }] },
            { physicalMembers: [{ available: true }, { available: true }] },
        ],
    }), {
        groupLabel: 'Group · 2',
        availabilityLabel: '',
        availabilityTitle: 'All 2 available',
        health: 'healthy',
        healthLabel: 'Healthy group',
    });
});

test('degraded cards report unavailable logical members without healthy N/N copy', () => {
    assert.deepEqual(zoneCardPresentation({
        memberCount: 2,
        physicalMemberCount: 2,
        availableMemberCount: 1,
        degraded: true,
        members: [
            { available: true, physicalMembers: [{ available: true }] },
            { available: false, physicalMembers: [{ available: false }] },
        ],
    }), {
        groupLabel: 'Group · 2',
        availabilityLabel: '1/2 available',
        availabilityTitle: '1 unavailable',
        health: 'degraded',
        healthLabel: 'Degraded group: 1 unavailable',
    });
});

test('degraded stereo zones expose physical loss separately from logical count', () => {
    assert.deepEqual(zoneCardPresentation({
        memberCount: 2,
        physicalMemberCount: 3,
        availableMemberCount: 2,
        degraded: true,
        members: [
            { available: true, physicalMembers: [{ available: true }] },
            {
                available: true,
                physicalMembers: [{ available: true }, { available: false }],
            },
        ],
    }), {
        groupLabel: 'Group · 2',
        availabilityLabel: '2/3 speakers available',
        availabilityTitle: '1 physical speaker unavailable',
        health: 'degraded',
        healthLabel: 'Degraded group: 1 physical speaker unavailable',
    });
});

test('member count summary mentions physical speakers only when counts differ', () => {
    assert.equal(zoneMemberCountSummary(2, 2), '2 members');
    assert.equal(zoneMemberCountSummary(2, 3), '2 members · 3 speakers');
    assert.equal(zoneMemberCountSummary(1, 2), '1 member · 2 speakers');
});

test('logical and physical metadata expose complete accessible identity', () => {
    assert.deepEqual(zoneMemberMetadata({
        controlId: 'living.local',
        ip: '192.0.2.20',
        name: 'Living room',
        model: 'SoundTouch 10',
        kind: 'stereoPair',
        connectivity: 'stale',
    }), {
        connectivity: 'stale',
        connectivityLabel: 'Stale',
        name: 'Living room',
        modelType: 'SoundTouch 10',
        ip: '192.0.2.20',
        kind: 'Stereo pair',
        statusAriaLabel: 'Living room: Stale',
    });

    const physical = physicalMemberMetadata({
        deviceId: 'right-id',
        role: 'right',
        ip: '192.0.2.21',
        name: 'Living right',
        type: 'SoundTouch 10',
        available: false,
        connectivity: 'offline',
    });
    assert.equal(physical.role, 'RIGHT');
    assert.equal(physical.statusAriaLabel, 'Living right: Offline');
    assert.equal(physical.ip, '192.0.2.21');
    assert.equal(physical.modelType, 'SoundTouch 10');
});

test('logical metadata resolves full IP from physical identity for hostname controls', () => {
    const metadata = zoneMemberMetadata({
        controlId: 'living.local',
        ip: 'living.local',
        hwId: 'right-id',
        name: 'Living room',
        kind: 'stereoPair',
        physicalMembers: [
            { deviceId: 'left-id', ip: '192.0.2.20' },
            { deviceId: 'right-id', ip: '192.0.2.21' },
        ],
    });

    assert.equal(metadata.ip, '192.0.2.21');
});
