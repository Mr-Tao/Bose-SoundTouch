const JSON_HEADERS = { 'Content-Type': 'application/json' };
const SETTINGS_TARGET_HEADER = 'X-AfterTouch-Settings-Target';

async function req(url, opts = {}) {
    const r = await fetch(url, opts);
    return r.json();
}

function settingsMutation(url, method, targetIdentity, body) {
    const headers = { [SETTINGS_TARGET_HEADER]: targetIdentity };
    const options = { method, headers };
    if (body !== undefined) {
        Object.assign(headers, JSON_HEADERS);
        options.body = JSON.stringify(body);
    }
    return req(url, options);
}

export const api = {
    devices: () => req('/api/control/devices'),
    device: (id) => req(`/api/control/devices/${id}`),
    removeDevice: (id) => req(`/api/control/devices/${id}`, { method: 'DELETE' }),
    settings: (id) => req(`/api/control/devices/${id}/settings/`),
    setClockDisplay: (id, targetIdentity, body) => settingsMutation(
        `/api/control/devices/${id}/settings/clock-display`, 'PATCH', targetIdentity, body),
    setClockTime: (id, targetIdentity) => settingsMutation(
        `/api/control/devices/${id}/settings/clock-time`, 'POST', targetIdentity),
    setSystemTimeout: (id, targetIdentity, enabled) => settingsMutation(
        `/api/control/devices/${id}/settings/system-timeout`, 'PATCH', targetIdentity, { enabled }),
    setLanguage: (id, targetIdentity, code) => settingsMutation(
        `/api/control/devices/${id}/settings/language`, 'PATCH', targetIdentity, { code }),
    setSync: (id, targetIdentity, mode) => settingsMutation(
        `/api/control/devices/${id}/settings/sync`, 'PATCH', targetIdentity, { mode }),
    bluetoothPair: (id, targetIdentity) => settingsMutation(
        `/api/control/devices/${id}/settings/bluetooth/pair`, 'POST', targetIdentity),
    clearBluetoothPairings: (id, targetIdentity) => settingsMutation(
        `/api/control/devices/${id}/settings/bluetooth/pairings?confirmed=true`, 'DELETE', targetIdentity),
    setSourceName: (id, targetIdentity, source, sourceAccount, name) => settingsMutation(
        `/api/control/devices/${id}/settings/source-name`, 'PATCH', targetIdentity,
        { source, sourceAccount, name }),
    discover: () => req('/api/control/discover', { method: 'POST' }),
    key: (id, key) => req(`/api/control/devices/${id}/key/${key}`, { method: 'POST' }),
    volume: (id, level) => req(`/api/control/devices/${id}/volume/${level}`, { method: 'POST' }),
    bass: (id, level) => req(`/api/control/devices/${id}/action/bass`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify({ level }),
    }),
    power: (id) => req(`/api/control/devices/${id}/power`, { method: 'POST' }),
    recents: (id) => req(`/api/control/devices/${id}/recents`),
    zone: (id) => req(`/api/control/devices/${id}/zone`),
    zoneCandidates: (id) => req(`/api/control/devices/${id}/zone/candidates`),
    zoneAdd: (masterId, slaveId) => req(`/api/control/devices/${masterId}/zone/add/${slaveId}`, { method: 'POST' }),
    zoneRemove: (masterId, slaveId) => req(`/api/control/devices/${masterId}/zone/remove/${slaveId}`, { method: 'POST' }),
    zoneDissolve: (id) => req(`/api/control/devices/${id}/zone/dissolve`, { method: 'POST' }),
    zoneLeave: (id) => req(`/api/control/devices/${id}/zone/leave`, { method: 'POST' }),
    play: (id, item) => req(`/api/control/devices/${id}/play`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify(item),
    }),
    tuneInBrowse: (path) => req(path ? `/api/control/providers/tunein/navigate/${path}` : '/api/control/providers/tunein/navigate'),
    tuneInSearch: (q) => req(`/api/control/providers/tunein/search?q=${encodeURIComponent(q)}`),
    tuneInSearchNext: (cursor) => req(`/api/control/providers/tunein/search/next?cursor=${encodeURIComponent(cursor)}`),
    control: (id, action, presetId) => req(`/api/control/devices/${id}/action/${action}?id=${presetId}`),
    storePreset: (id, slotId) => req(`/api/control/devices/${id}/action/storepreset?id=${slotId}`),
    selectSource: (id, source, account) => req(`/api/control/devices/${id}/action/source?name=${encodeURIComponent(source)}&account=${encodeURIComponent(account || '')}`),
    tuneInPlay: (deviceId, item) => req(`/api/control/devices/${deviceId}/providers/tunein/play`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify(item),
    }),
    radioBrowserSearch: (q) => req(`/api/control/providers/radiobrowser/search?q=${encodeURIComponent(q)}`),
    radioBrowserPlay: (deviceId, item) => req(`/api/control/devices/${deviceId}/providers/radiobrowser/play`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify(item),
    }),
    playURL: (deviceId, url, name, imageUrl, serviceUrl) => req(`/api/control/devices/${deviceId}/providers/url/play`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify({ url, name, imageUrl, serviceUrl }),
    }),
    speak: (deviceId, text) => req(`/api/control/devices/${deviceId}/providers/tts/play`, {
        method: 'POST',
        headers: JSON_HEADERS,
        body: JSON.stringify({ text }),
    }),
    libraryDiscover: (timeout) => req(`/api/control/providers/library/servers${timeout ? `?timeout=${timeout}` : ''}`),
    libraryServers: (id) => req(`/api/control/devices/${id}/library/servers`),
    libraryAddServer: (id, body) => req(`/api/control/devices/${id}/library/servers`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) }),
    libraryRemoveServer: (id, account) => req(`/api/control/devices/${id}/library/servers/${encodeURIComponent(account)}`, { method: 'DELETE' }),
    libraryBrowse: (id, { account, location, type, start, count }) => {
        const qs = [
            `account=${encodeURIComponent(account)}`,
            location !== undefined && location !== '' ? `location=${encodeURIComponent(location)}` : null,
            type ? `type=${encodeURIComponent(type)}` : null,
            start !== undefined ? `start=${encodeURIComponent(start)}` : null,
            count !== undefined ? `count=${encodeURIComponent(count)}` : null,
        ].filter(Boolean).join('&');
        return req(`/api/control/devices/${id}/library/browse?${qs}`);
    },
    libraryPlay: (id, body) => req(`/api/control/devices/${id}/library/play`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) }),
};
