import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';

const html = htm.bind(h);

function isStereoCapable(device) {
    const type = (device?.info?.type || '').trim().toLowerCase();
    return type === 'st10' || type === 'soundtouch 10';
}

function responseError(resp, fallback) {
    const details = (resp?.data?.members || []).flatMap(member => [
        member.preflightError,
        member.mutationError,
        member.verificationError,
        member.compensationError,
    ].filter(Boolean).map(message => `${member.ipAddress || member.deviceId || 'speaker'}: ${message}`));

    return [resp?.error || fallback, resp?.data?.persistenceError, ...details].filter(Boolean).join('; ');
}

function groupId(group) {
    return group?.ID || group?.id || '';
}

function groupRoles(group) {
    return group?.Roles?.Roles || group?.roles?.roles || [];
}

function hasConfiguredGroup(group) {
    return Boolean(group && (groupId(group) || group?.MasterDeviceID || group?.masterDeviceId || groupRoles(group).length));
}

function snapshotFromProjection(pair) {
    if (!pair?.id) return null;
    return {
        ID: pair.id,
        Name: pair.name || '',
        MasterDeviceID: pair.masterDeviceId,
        Roles: {
            Roles: (pair.members || []).map(member => ({
                DeviceID: member.deviceId,
                Role: member.role,
                IPAddress: member.ipAddress,
            })),
        },
    };
}

export function StereoPair({ deviceId, device, devices, onChanged, notify }) {
    const pair = device?.stereoPair;
    const [state, setState] = useState(null);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');
    const [showPicker, setShowPicker] = useState(false);
    const [rightId, setRightId] = useState('');
    const [name, setName] = useState(pair?.name || device?.info?.name || '');
    const [savedRecovery, setSavedRecovery] = useState(null);
    const refreshGeneration = useRef(0);
    const nameEdited = useRef(false);
    const mutationError = useRef('');
    const previousDeviceId = useRef(deviceId);
    const mounted = useRef(true);
    const currentSelection = useRef({ deviceId });
    if (currentSelection.current.deviceId !== deviceId) {
        currentSelection.current = { deviceId };
    }
    const refreshKey = JSON.stringify([deviceId, pair?.id || null, pair?.name || null]);
    const currentRefreshKey = useRef(refreshKey);
    currentRefreshKey.current = refreshKey;

    useEffect(() => {
        mounted.current = true;
        return () => {
            mounted.current = false;
        };
    }, []);

    async function refresh({ preserveError = false } = {}) {
        if (refreshKey !== currentRefreshKey.current) return;
        const generation = ++refreshGeneration.current;
        try {
            const resp = await api.stereoPair(deviceId);
            if (generation !== refreshGeneration.current || refreshKey !== currentRefreshKey.current) return;
            if (resp?.data) {
                setState(resp.data);
                const currentName = resp.data.group?.Name || resp.data.group?.name;
                if (currentName && !nameEdited.current) setName(currentName);
            }
            if (resp?.success) {
                if (!preserveError && !mutationError.current) setError('');
            } else if (!preserveError && !mutationError.current) {
                setError(responseError(resp, 'Unable to read stereo-pair state'));
            }
        } catch (_) {
            if (generation === refreshGeneration.current && refreshKey === currentRefreshKey.current &&
                !preserveError && !mutationError.current) {
                setError('Unable to read stereo-pair state');
            }
        }
    }

    useEffect(() => {
        if (previousDeviceId.current !== deviceId) {
            previousDeviceId.current = deviceId;
            nameEdited.current = false;
            mutationError.current = '';
            setState(null);
            setError('');
            setBusy(false);
            setSavedRecovery(null);
        }
        if (!nameEdited.current) setName(pair?.name || device?.info?.name || '');
        setRightId('');
        setShowPicker(false);
        refresh();
        return () => {
            refreshGeneration.current++;
        };
    }, [deviceId, pair?.id, pair?.name]);

    const stateGroup = state?.group;
    const stateSnapshot = hasConfiguredGroup(stateGroup) && (!pair?.id || groupId(stateGroup) === pair.id)
        ? stateGroup : null;
    const observedSnapshot = stateSnapshot || snapshotFromProjection(pair);
    const savedSnapshot = savedRecovery?.deviceId === deviceId ? savedRecovery.group : null;
    const recoverySnapshot = observedSnapshot || savedSnapshot;
    const recoveryGroupRoles = groupRoles(recoverySnapshot);
    const recoveryGroupId = groupId(recoverySnapshot);
    const recoveryGroup = !pair && hasConfiguredGroup(recoverySnapshot);
    const expectedGroupId = pair?.id || recoveryGroupId;

    const candidates = Object.entries(devices || {}).filter(([id, candidate]) =>
        id !== deviceId && !candidate.stereoPair && !(candidate.status?.group?.ID || candidate.status?.group?.id) &&
        candidate.status?.isConnected && isStereoCapable(candidate));

    async function recoverFromFailure(operationError) {
        mutationError.current = operationError;
        setError(operationError);
        await Promise.allSettled([
            Promise.resolve().then(() => onChanged?.()),
            refresh({ preserveError: true }),
        ]);
    }

    async function run(action, successMessage, onSuccess) {
        const selection = currentSelection.current;
        const isCurrentMutation = () => mounted.current && selection === currentSelection.current;
        setBusy(true);
        mutationError.current = '';
        setError('');
        try {
            const resp = await action();
            if (!isCurrentMutation()) return;
            refreshGeneration.current++;
            if (!resp?.success) {
                await recoverFromFailure(responseError(resp, 'Stereo-pair operation failed'));
                return;
            }
            nameEdited.current = false;
            setShowPicker(false);
            onSuccess?.();
            notify?.(successMessage);
            await onChanged?.();
            await refresh();
        } catch (_) {
            if (isCurrentMutation()) {
                await recoverFromFailure('Stereo-pair operation failed');
            }
        } finally {
            if (isCurrentMutation()) setBusy(false);
        }
    }

    function editName(event) {
        nameEdited.current = true;
        setName(event.currentTarget.value);
    }

    function createPair() {
        if (!rightId) return;
        run(() => api.stereoPairCreate(deviceId, rightId, name.trim()), 'Stereo pair created');
    }

    function renamePair(event) {
        event.preventDefault();
        const nextName = name.trim();
        if (!expectedGroupId || !nextName || nextName === pair?.name) return;
        run(() => api.stereoPairRename(deviceId, expectedGroupId, nextName), 'Stereo pair renamed');
    }

    function dissolvePair() {
        if (!expectedGroupId || !recoverySnapshot) return;
        const pairName = pair?.name || recoverySnapshot?.Name || recoverySnapshot?.name ||
            device?.info?.name || 'this stereo pair';
        const recovering = !pair && recoveryGroup;
        const prompt = recovering
            ? `Continue cleanup for "${pairName}"?\n\nThe service will recheck the saved generation and finish any remaining teardown or persistence cleanup.`
            : `Dissolve "${pairName}"?\n\nBoth speakers will become standalone devices.`;
        if (!confirm(prompt)) return;
        setSavedRecovery({ deviceId, group: recoverySnapshot });
        run(
            () => api.stereoPairDissolve(deviceId, expectedGroupId, recoverySnapshot),
            recovering ? 'Stereo pair cleanup completed' : 'Stereo pair dissolved',
            () => setSavedRecovery(null),
        );
    }

    if (!pair && !recoveryGroup && state && !state.capable) return null;
    if (!pair && !state && !isStereoCapable(device)) return null;

    return html`
        <section class="stereo-pair-section">
            <div class="section-title">Stereo pair</div>
            ${error ? html`<div class="stereo-pair-error" role="alert">${error}</div>` : null}

            ${pair ? html`
                <div class="stereo-pair-members">
                    ${(pair.members || []).map(member => html`
                        <div class="stereo-pair-member" key=${member.deviceId}>
                            <span class="stereo-role">${member.role}</span>
                            <span class="stereo-member-name">${member.name || member.ipAddress || member.deviceId}</span>
                            <span class="device-indicator ${member.available ? 'online' : 'offline'}"
                                title=${member.available ? 'Online' : 'Unavailable'}></span>
                        </div>
                    `)}
                </div>
                <form class="stereo-pair-actions" onSubmit=${renamePair}>
                    <label class="stereo-name-field">
                        <span>Name</span>
                        <input value=${name} onInput=${editName}
                            disabled=${busy} maxlength="64" />
                    </label>
                    <button class="btn-secondary stereo-action" type="submit"
                        disabled=${busy || !expectedGroupId || !name.trim() || name.trim() === pair.name}>Rename</button>
                    <button class="btn-secondary stereo-action danger" type="button"
                        onClick=${dissolvePair} disabled=${busy || !expectedGroupId}>Dissolve</button>
                </form>
            ` : recoveryGroup ? html`
                <div class="stereo-pair-members">
                    <div class="stereo-pair-member">
                        <span class="stereo-role">Recovery required</span>
                        <span class="stereo-member-name">${recoverySnapshot.Name || recoverySnapshot.name || 'Unnamed stereo pair'}</span>
                    </div>
                    <div class="stereo-pair-member">
                        <span class="stereo-role">Generation ID</span>
                        <span class="stereo-member-name">${recoveryGroupId}</span>
                    </div>
                    ${recoveryGroupRoles.map(member => html`
                        <div class="stereo-pair-member" key=${member.DeviceID || member.deviceId || member.Role || member.role}>
                            <span class="stereo-role">${member.Role || member.role || 'Member'}</span>
                            <span class="stereo-member-name">${member.IPAddress || member.ipAddress || member.DeviceID || member.deviceId || 'Unknown member'}</span>
                        </div>
                    `)}
                </div>
                <div class="stereo-pair-actions">
                    <button class="btn-secondary stereo-action danger" type="button"
                        onClick=${dissolvePair} disabled=${busy || !expectedGroupId}>Continue cleanup</button>
                </div>
            ` : html`
                <div class="stereo-pair-standalone">
                    <span>Standalone</span>
                    <button class="btn-secondary stereo-action" onClick=${() => setShowPicker(true)}
                        disabled=${busy || !state?.capable || candidates.length === 0}>Create stereo pair</button>
                </div>
            `}

            ${showPicker ? html`
                <div class="overlay" onClick=${() => setShowPicker(false)}>
                    <div class="device-picker stereo-picker" onClick=${event => event.stopPropagation()}>
                        <div class="picker-title">Create stereo pair</div>
                        <label class="stereo-name-field picker-name-field">
                            <span>Name</span>
                            <input value=${name} onInput=${editName} maxlength="64" />
                        </label>
                        <div class="picker-label">Right speaker</div>
                        <div class="picker-devices">
                            ${candidates.map(([id, candidate]) => html`
                                <button class="picker-device-btn ${rightId === id ? 'selected' : ''}"
                                    type="button" key=${id} onClick=${() => setRightId(id)}>
                                    <div class="picker-device-info">
                                        <span class="picker-device-name">${candidate.info?.name || id}</span>
                                        <span class="picker-device-ip">${candidate.info?.ip_address || id}</span>
                                    </div>
                                </button>
                            `)}
                        </div>
                        <div class="stereo-picker-actions">
                            <button class="btn-secondary" type="button" onClick=${() => setShowPicker(false)}>Cancel</button>
                            <button class="btn-primary" type="button" onClick=${createPair}
                                disabled=${busy || !rightId || !name.trim()}>Create</button>
                        </div>
                    </div>
                </div>
            ` : null}
        </section>
    `;
}
