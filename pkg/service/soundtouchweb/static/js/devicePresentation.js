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

function compareText(a, b) {
    return String(a).localeCompare(String(b), undefined, {
        numeric: true,
        sensitivity: 'base',
    });
}

function displayName(id, device) {
    return String(device?.info?.name || '').trim() || id;
}

function presentationAddress(id, device) {
    return String(device?.info?.ip_address || '').trim() || id;
}

export function sortDeviceEntries(entries, mode) {
    const sorted = [...entries];

    sorted.sort(([idA, deviceA], [idB, deviceB]) => {
        const primary = mode === 'name'
            ? compareText(displayName(idA, deviceA), displayName(idB, deviceB))
            : compareText(presentationAddress(idA, deviceA), presentationAddress(idB, deviceB));

        return primary || compareText(idA, idB);
    });

    return sorted;
}
