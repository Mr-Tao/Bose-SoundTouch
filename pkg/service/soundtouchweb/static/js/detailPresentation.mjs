import { zoneMembershipPresentation } from './zonePresentation.mjs';
import { connectivityState, nowPlayingFreshness } from './devicePresentation.js';

function text(value) {
    return typeof value === 'string' ? value.trim() : '';
}

function positiveCount(value) {
    return Number.isSafeInteger(value) && value > 0 ? value : null;
}

// Use the same projected target as Controls. A membership names a group but
// never turns the selected member's direct volume control into a group write.
export function detailVolumeLabel(deviceId, device) {
    if (device?.zone && !device.zone.isStandalone && device.zone.masterControlId === deviceId) {
        return 'Group volume';
    }
    return device?.zoneMembership ? 'Member volume' : 'Volume';
}

export function libraryOwnerCaption(tab, deviceId, device) {
    if (tab === 'favorites') return 'Shared library';
    const physicalID = text(device?.info?.device_id);
    const pair = device?.stereoPair;
    const selected = Array.isArray(pair?.members) && physicalID
        ? pair.members.filter(member => text(member?.deviceId) === physicalID) : [];
    // Pair projection replaces info.name with the logical name. Hardware
    // presets and history still belong to the physical command target.
    const owner = (pair
        ? (selected.length === 1 ? text(selected[0].name) : '')
        : text(device?.info?.name)) || physicalID || text(deviceId) || 'Selected speaker';
    return `${tab === 'recents' ? 'Recents' : 'Presets'} · ${owner}`;
}

export function detailFreshnessPresentation(device) {
    const status = device?.status;
    const known = ['online', 'stale', 'offline'].includes(status?.connectivity) ||
        typeof status?.isConnected === 'boolean';
    const state = known ? connectivityState(device) : 'unknown';
    const label = state.charAt(0).toUpperCase() + state.slice(1);
    const freshness = known ? nowPlayingFreshness(device) : {
        live: false, label: 'Last known', title: 'Live playback has not been confirmed',
    };
    const playback = status?.nowPlaying;
    return {
        state, label,
        freshness: playback?.Source && playback.Source !== 'STANDBY' && !freshness.live
            ? freshness : null,
    };
}

function groupSummary(zone) {
    const members = Array.isArray(zone.members) ? zone.members : [];
    const logical = positiveCount(zone.memberCount) || positiveCount(members.length);
    // Physical count is explicit, or derived only when every member has a
    // complete set of physical identities. Do not assume one speaker per member.
    const completePhysical = members.length > 0 && members.every(member =>
        Array.isArray(member?.deviceIds) && member.deviceIds.length > 0 &&
        member.deviceIds.every(id => text(id)));
    const physicalIDs = completePhysical ? members.flatMap(member => member.deviceIds) : [];
    const derivedPhysical = completePhysical && new Set(physicalIDs).size === physicalIDs.length
        ? positiveCount(physicalIDs.length) : null;
    const physical = positiveCount(zone.physicalMemberCount) || derivedPhysical;
    const parts = ['Group'];
    if (logical) parts.push(`${logical} ${logical === 1 ? 'member' : 'members'}`);
    if (physical && (!logical || physical >= logical)) {
        parts.push(`${physical} ${physical === 1 ? 'speaker' : 'speakers'}`);
    }
    return parts.join(' · ');
}

export function detailPresentation(deviceId, device) {
    const pair = device?.stereoPair;
    const group = detailVolumeLabel(deviceId, device) === 'Group volume';
    const membership = group ? null : zoneMembershipPresentation(device?.zoneMembership);
    const name = text(pair?.name) || text(device?.info?.name) || text(deviceId) || 'Speaker';
    const model = text(device?.info?.type) || 'Unknown model';
    const pairIDs = Array.isArray(pair?.members)
        ? pair.members.map(member => text(member?.deviceId)).filter(Boolean) : [];
    const confirmedPairCount = pairIDs.length === 2 && new Set(pairIDs).size === 2;
    const context = group ? groupSummary(device.zone)
        : pair ? `Stereo pair${confirmedPairCount ? ' · 2 speakers' : ''}` : model;
    const degraded = group ? Boolean(device.zone.degraded)
        : Boolean(pair?.degraded || device?.zoneMembership?.degraded);

    return { name, context, membership, degraded };
}
