export function deviceSoundTarget(device, member) {
    return device?.deviceSettingsTarget || member?.deviceSettingsTarget || null;
}

export function targetBassStatus(target) {
    if (!target) return {};

    return {
        bass: target.bass,
        bassCapabilities: target.bassCapabilities,
        bassRevision: target.bassRevision,
        connectivity: target.connectivity,
        isConnected: target.connectivity !== 'offline',
    };
}

export function stereoPairPresentation(controlId, device, member) {
    const pair = device?.stereoPair || member?.stereoPair;
    if (!pair) return null;

    return {
        controlId,
        name: pair.name || device?.info?.name || member?.name || controlId,
        device: device || {
            stereoPair: pair,
            status: {
                balance: member?.balance,
                balanceRevision: member?.balanceRevision,
                connectivity: member?.connectivity,
                isConnected: member?.available,
            },
        },
    };
}

export function steppedValue(value, delta, min, max) {
    if (![value, delta, min, max].every(Number.isSafeInteger) || min > max) return null;
    const next = Math.max(min, Math.min(max, value + delta));
    return next === value ? null : next;
}

export function resetValue(value, defaultValue, min, max) {
    if (![value, defaultValue, min, max].every(Number.isSafeInteger) ||
        min > max || defaultValue < min || defaultValue > max || value === defaultValue) {
        return null;
    }
    return defaultValue;
}

export function confirmedSettingReadback(response, requested, min, max, afterRevision = -1) {
    const data = response?.data;
    if (!response?.success || data?.requested !== requested || !Number.isSafeInteger(data?.actual) ||
        !Number.isSafeInteger(data?.revision) || data.revision <= afterRevision ||
        !Number.isSafeInteger(min) || !Number.isSafeInteger(max) || data.actual < min || data.actual > max) {
        return null;
    }
    return { actual: data.actual, revision: data.revision };
}

export function confirmedSettingActual(response, requested, min, max, afterRevision = -1) {
    return confirmedSettingReadback(response, requested, min, max, afterRevision)?.actual ?? null;
}

export function shouldClearSettingFailure({
    failedRevision,
    projectionRevision,
    available,
    projectedValue,
    busy,
}) {
    return failedRevision !== null && Number.isSafeInteger(projectionRevision) &&
        projectionRevision > failedRevision && available &&
        Number.isSafeInteger(projectedValue) && !busy;
}

export function newerSettingProjection({
    projectionRevision,
    projectedValue,
    available,
    afterRevision,
}) {
    if (!Number.isSafeInteger(projectionRevision) || projectionRevision <= afterRevision ||
        !available || !Number.isSafeInteger(projectedValue)) {
        return null;
    }

    return { actual: projectedValue, revision: projectionRevision };
}

export function settingFailureMessage(response, noun) {
    if (!response?.success) return response?.error || `${noun} update failed.`;
    if (response?.data?.atTarget === false) {
        return `Speaker reported ${response.data.actual}; requested ${response.data.requested}.`;
    }
    return '';
}

export function formatBassValue(value) {
    return value > 0 ? `+${value}` : String(value);
}

export function formatBalanceValue(value) {
    if (value < 0) return `+${Math.abs(value)} Left`;
    if (value > 0) return `+${value} Right`;
    return 'Centered';
}
