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

// nowPlayingFreshness says whether a card may present its now-playing as
// live. Once a speaker is stale or offline, the status it shows is only the
// last one it reported, and a play glyph would claim otherwise.
export function nowPlayingFreshness(device) {
    const state = connectivityState(device);
    if (state === 'online') return { live: true, label: '', title: '' };

    return {
        live: false,
        label: 'Last known',
        title: state === 'offline'
            ? 'Last reported before the speaker went offline'
            : 'Not confirmed since the speaker missed an update',
    };
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

// previousDetailTarget picks where Back leads from a device-detail page that
// was opened from a zone's member rows: the most recent origin that is still
// in the inventory. Origins that disappeared meanwhile are skipped. An empty
// id means Back returns to the device list.
export function previousDetailTarget(origins, devices) {
    const remaining = [...(origins || [])];
    while (remaining.length > 0) {
        const id = remaining.pop();
        if (id && devices && Object.prototype.hasOwnProperty.call(devices, id)) {
            return { id, origins: remaining };
        }
    }

    return { id: null, origins: [] };
}
