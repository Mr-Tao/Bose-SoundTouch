import { clampVolume } from './zoneVolumeResult.mjs';

export function zoneMemberVolumes(zone) {
    const volumes = {};

    for (const member of zone?.members || []) {
        const controlId = member?.controlId || member?.ip;
        if (member?.actualVolume === null || member?.actualVolume === undefined ||
            member?.actualVolume === '') continue;
        const volume = Number(member?.actualVolume);
        if (!controlId || !Number.isFinite(volume)) continue;

        volumes[controlId] = clampVolume(Math.round(volume));
    }

    return volumes;
}

export function maxZoneVolume(volumes) {
    const values = Object.values(volumes || {}).filter(volume => Number.isFinite(volume));

    return values.length > 0 ? Math.max(...values) : 0;
}

export function previewZoneVolume(startingVolumes, startingLevel, requested) {
    const entries = Object.entries(startingVolumes || {})
        .filter(([, volume]) => Number.isFinite(volume));
    if (entries.length === 0) return {};

    const delta = clampVolume(requested) - clampVolume(startingLevel);

    return Object.fromEntries(entries.map(([controlId, volume]) => [
        controlId,
        clampVolume(volume + delta),
    ]));
}

export function mergeZoneVolumeReadback(currentVolumes, data) {
    const volumes = { ...(currentVolumes || {}) };

    for (const member of data?.members || []) {
        const controlId = member?.controlId;
        if (!controlId || !Number.isFinite(member?.actual)) continue;

        volumes[controlId] = clampVolume(member.actual);
    }

    return volumes;
}

export function sameZoneMemberVolumes(left, right) {
    const leftEntries = Object.entries(left || {}).sort(([a], [b]) => a.localeCompare(b));
    const rightEntries = Object.entries(right || {}).sort(([a], [b]) => a.localeCompare(b));

    if (leftEntries.length !== rightEntries.length) return false;

    return leftEntries.every(([controlId, volume], index) =>
        rightEntries[index][0] === controlId && rightEntries[index][1] === volume);
}
