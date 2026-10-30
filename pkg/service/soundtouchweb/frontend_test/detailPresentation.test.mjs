import assert from 'node:assert/strict';
import test from 'node:test';
import {
    detailPresentation,
    detailFreshnessPresentation,
    detailVolumeLabel,
    libraryOwnerCaption,
} from '../static/js/detailPresentation.mjs';

const id = '192.0.2.10';
const speaker = () => ({ info: { name: 'Bedroom', type: 'SoundTouch 20', device_id: 'left' } });
const pair = () => ({
    ...speaker(),
    info: { ...speaker().info, name: 'Stereo room' },
    stereoPair: { name: 'Stereo room', members: [
        { deviceId: 'left', name: 'Bedroom' }, { deviceId: 'right', name: 'Bedroom right' },
    ] },
});
const zone = () => ({
    masterControlId: id, memberCount: 2, physicalMemberCount: 3,
    members: [{ deviceIds: ['left'] }, { deviceIds: ['pair-left', 'pair-right'] }],
});

test('standalone detail names the speaker and model', () => {
    assert.deepEqual(detailPresentation(id, speaker()), {
        name: 'Bedroom', context: 'SoundTouch 20', membership: null, degraded: false,
    });
    assert.equal(detailVolumeLabel(id, speaker()), 'Volume');
});

test('stereo detail uses the logical pair name and confirmed physical count', () => {
    const device = pair();
    assert.equal(detailPresentation(id, device).name, 'Stereo room');
    assert.equal(detailPresentation(id, device).context, 'Stereo pair · 2 speakers');
    assert.equal(detailVolumeLabel(id, device), 'Volume');
    device.stereoPair.degraded = true;
    device.stereoPair.members[1].available = false;
    assert.equal(detailPresentation(id, device).degraded, true);
    assert.equal(detailPresentation(id, device).context, 'Stereo pair · 2 speakers');
});

test('group detail distinguishes logical members and physical speakers', () => {
    const device = { ...speaker(), zone: zone() };
    assert.equal(detailPresentation(id, device).name, 'Bedroom');
    assert.equal(detailPresentation(id, device).context, 'Group · 2 members · 3 speakers');
    assert.equal(detailVolumeLabel(id, device), 'Group volume');
    delete device.zone.physicalMemberCount;
    assert.equal(detailPresentation(id, device).context, 'Group · 2 members · 3 speakers');
    device.zone.members[1].deviceIds = ['other'];
    assert.equal(detailPresentation(id, device).context, 'Group · 2 members · 2 speakers');
    device.zone.degraded = true;
    assert.equal(detailPresentation(id, device).degraded, true);
});

test('member detail retains its own identity and direct volume scope', () => {
    const device = { ...pair(), zoneMembership: { masterControlId: '192.0.2.20', masterName: 'Kitchen' } };
    assert.equal(detailPresentation(id, device).name, 'Stereo room');
    assert.equal(detailPresentation(id, device).context, 'Stereo pair · 2 speakers');
    assert.equal(detailPresentation(id, device).membership.label, 'In group · Kitchen');
    assert.equal(detailVolumeLabel(id, device), 'Member volume');
    device.zoneMembership.degraded = true;
    assert.equal(detailPresentation(id, device).degraded, true);
});

test('library ownership stays with the selected physical speaker in every context', () => {
    for (const device of [speaker(), pair(), { ...pair(), zone: zone() },
        { ...speaker(), zoneMembership: { masterName: 'Kitchen' } }]) {
        assert.equal(libraryOwnerCaption('presets', id, device), 'Presets · Bedroom');
        assert.equal(libraryOwnerCaption('recents', id, device), 'Recents · Bedroom');
        assert.equal(libraryOwnerCaption('favorites', id, device), 'Shared library');
    }
    const device = pair();
    device.info.device_id = 'right';
    assert.equal(libraryOwnerCaption('recents', id, device), 'Recents · Bedroom right');
    device.stereoPair.members[1].name = '';
    assert.equal(libraryOwnerCaption('presets', id, device), 'Presets · right');
    device.stereoPair.members.push({ deviceId: 'right', name: 'Ambiguous' });
    assert.equal(libraryOwnerCaption('presets', id, device), 'Presets · right');
    device.info.device_id = 'unregistered';
    assert.equal(libraryOwnerCaption('recents', id, device), 'Recents · unregistered');
});

test('detail freshness distinguishes live, last-known and unknown playback', () => {
    const device = { ...speaker(), status: { nowPlaying: { Source: 'INTERNET_RADIO' } } };
    for (const state of ['online', 'stale', 'offline']) {
        device.status.connectivity = state;
        const result = detailFreshnessPresentation(device);
        assert.equal(result.state, state);
        assert.equal(result.freshness?.label || '', state === 'online' ? '' : 'Last known');
    }
    delete device.status.connectivity;
    assert.equal(detailFreshnessPresentation(device).state, 'unknown');
    assert.equal(detailFreshnessPresentation(device).freshness.label, 'Last known');
    device.status.isConnected = true;
    assert.equal(detailFreshnessPresentation(device).state, 'online');
    assert.equal(detailFreshnessPresentation(device).freshness, null);
    device.status.isConnected = false;
    assert.equal(detailFreshnessPresentation(device).state, 'offline');
    device.status.nowPlaying.Source = 'STANDBY';
    assert.equal(detailFreshnessPresentation(device).freshness, null);
    assert.deepEqual(detailFreshnessPresentation(null), { state: 'unknown', label: 'Unknown', freshness: null });
});

test('missing and malformed metadata does not invent names, topology counts or group targets', () => {
    assert.deepEqual(detailPresentation('', null), {
        name: 'Speaker', context: 'Unknown model', membership: null, degraded: false,
    });
    assert.equal(libraryOwnerCaption('presets', id, { stereoPair: { name: 'Pair name' } }), `Presets · ${id}`);
    assert.equal(detailPresentation(id, { ...speaker(), stereoPair: { members: [] } }).context, 'Stereo pair');
    assert.equal(detailPresentation(id, { ...speaker(), stereoPair: {
        members: [{ deviceId: 'same' }, { deviceId: 'same' }],
    } }).context, 'Stereo pair');
    const device = { ...speaker(), zone: { masterControlId: id, members: {} } };
    assert.equal(detailPresentation(id, device).context, 'Group');
    device.zone = { masterControlId: id, memberCount: 2, members: [{}, {}] };
    assert.equal(detailPresentation(id, device).context, 'Group · 2 members');
    device.zone = { masterControlId: '192.0.2.99', memberCount: 2 };
    assert.equal(detailVolumeLabel(id, device), 'Volume');
    assert.equal(detailPresentation(id, device).context, 'SoundTouch 20');
    device.zone = { masterControlId: id, isStandalone: true };
    assert.equal(detailVolumeLabel(id, device), 'Volume');
    device.zoneMembership = { masterName: ' ', masterControlId: '' };
    assert.equal(detailPresentation(id, device).membership, null);
});

test('presentation does not mutate source data', () => {
    const device = { ...pair(), zone: zone() };
    const before = structuredClone(device);
    detailPresentation(id, device);
    detailVolumeLabel(id, device);
    libraryOwnerCaption('presets', id, device);
    assert.deepEqual(device, before);
});
