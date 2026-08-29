const connectivityStates = new Set(['online', 'stale', 'offline']);

export function connectivityState(device) {
    const reported = device?.status?.connectivity;
    if (connectivityStates.has(reported)) return reported;

    return device?.status?.isConnected ? 'online' : 'offline';
}

export function connectivityLabel(device) {
    const state = connectivityState(device);
    return state.charAt(0).toUpperCase() + state.slice(1);
}

export function deviceAddress(controlID, device) {
    return device?.info?.ip_address || controlID;
}

export function sortDeviceEntries(entries, mode) {
    const copy = [...entries];
    if (mode === 'name') {
        copy.sort(([idA, a], [idB, b]) =>
            (a?.info?.name || idA).localeCompare(b?.info?.name || idB, undefined, {
                sensitivity: 'base',
            }));
    } else {
        copy.sort(([idA, a], [idB, b]) =>
            deviceAddress(idA, a).localeCompare(deviceAddress(idB, b), undefined, {
                numeric: true,
                sensitivity: 'base',
            }));
    }
    return copy;
}

export function zoneMemberControlID(member) {
    return member?.controlId || member?.ip;
}

export function currentZoneMember(projection, member) {
    const controlID = zoneMemberControlID(member);
    const deviceIDs = new Set(member?.deviceIds || []);

    return (projection?.members || []).find(candidate =>
        candidate.controlId === controlID ||
        candidate.deviceIds?.some(deviceID => deviceIDs.has(deviceID))) || member;
}

export function resolvedZoneMember(projection, member) {
    const current = currentZoneMember(projection, member);
    const controlID = zoneMemberControlID(current) || zoneMemberControlID(member);

    return {
        member: current,
        controlId: controlID,
        name: current?.name || member?.name || controlID || '',
    };
}
