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

export function actualVolume(device) {
    const reported = device?.status?.volume?.ActualVolume;
    if (reported === null || reported === undefined || reported === '') return null;

    const value = Number(reported);
    if (!Number.isFinite(value)) return null;

    return Math.max(0, Math.min(100, Math.round(value)));
}

export function zoneMemberPresentation(member) {
    const reported = member?.connectivity;
    const connectivity = connectivityStates.has(reported)
        ? reported
        : (member?.available ? 'online' : 'offline');
    const volume = actualVolume({ status: { volume: { ActualVolume: member?.actualVolume } } });

    return {
        connectivity,
        label: connectivity.charAt(0).toUpperCase() + connectivity.slice(1),
        role: 'status',
        volume,
    };
}

export function currentZoneMember(projection, member) {
    const controlId = zoneMemberControlID(member);
    const deviceIds = new Set(member?.deviceIds || []);

    return (projection?.members || []).find(candidate =>
        candidate.controlId === controlId ||
        candidate.deviceIds?.some(deviceId => deviceIds.has(deviceId))) || member;
}

export function resolvedZoneMember(projection, member) {
    const current = currentZoneMember(projection, member);
    const controlId = zoneMemberControlID(current) || zoneMemberControlID(member);

    return {
        member: current,
        controlId,
        name: current?.name || member?.name || controlId || '',
    };
}

export function zoneMemberControlID(member) {
    return member?.controlId || member?.ip;
}
