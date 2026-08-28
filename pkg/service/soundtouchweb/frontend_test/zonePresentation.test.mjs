import assert from 'node:assert/strict';
import test from 'node:test';

import {
    physicalMemberMetadata,
    zoneCardPresentation,
    zoneMemberCountSummary,
    zoneMemberMetadata,
} from '../static/js/zonePresentation.mjs';

test('healthy zone cards show only the logical group count', () => {
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
    });
});

test('degraded zone cards report logical availability without repeating status', () => {
    assert.deepEqual(zoneCardPresentation({
        memberCount: 2,
        physicalMemberCount: 3,
        availableMemberCount: 1,
        degraded: true,
        members: [
            { physicalMembers: [{ available: true }] },
            { physicalMembers: [{ available: true }, { available: false }] },
        ],
    }), {
        groupLabel: 'Group · 2',
        availabilityLabel: '1/2 available',
        availabilityTitle: '1 unavailable',
    });
});

test('degraded stereo zones report physical loss without claiming all is available', () => {
    assert.deepEqual(zoneCardPresentation({
        memberCount: 2,
        physicalMemberCount: 3,
        availableMemberCount: 2,
        degraded: true,
        members: [
            { physicalMembers: [{ available: true }] },
            { physicalMembers: [{ available: true }, { available: false }] },
        ],
    }), {
        groupLabel: 'Group · 2',
        availabilityLabel: '2/3 speakers available',
        availabilityTitle: '1 physical speaker unavailable',
    });
});

test('count summary mentions physical speakers only when counts differ', () => {
    assert.equal(zoneMemberCountSummary(2, 2), '2 members');
    assert.equal(zoneMemberCountSummary(2, 3), '2 members · 3 speakers');
    assert.equal(zoneMemberCountSummary(1, 2), '1 member · 2 speakers');
});

test('logical member metadata includes diagnostics and accessible labels', () => {
    assert.deepEqual(zoneMemberMetadata({
        controlId: '192.0.2.20',
        ip: '192.0.2.20',
        name: 'Living room',
        type: 'SoundTouch 10',
        kind: 'stereoPair',
        connectivity: 'stale',
        actualVolume: 35,
    }), {
        connectivity: 'stale',
        label: 'Stale',
        role: 'status',
        volume: 35,
        name: 'Living room',
        type: 'SoundTouch 10',
        ip: '192.0.2.20',
        kind: 'Stereo pair',
        statusAriaLabel: 'Living room: Stale',
        volumeAriaLabel: 'Living room volume',
    });
});

test('physical metadata exposes stereo role without creating a control label', () => {
    const metadata = physicalMemberMetadata({
        deviceId: 'right-id',
        role: 'right',
        ip: '192.0.2.21',
        name: 'Living right',
        type: 'SoundTouch 10',
        available: false,
        connectivity: 'offline',
    });

    assert.equal(metadata.role, 'RIGHT');
    assert.equal(metadata.statusAriaLabel, 'Living right: Offline');
    assert.equal(metadata.ip, '192.0.2.21');
    assert.equal(metadata.type, 'SoundTouch 10');
});
