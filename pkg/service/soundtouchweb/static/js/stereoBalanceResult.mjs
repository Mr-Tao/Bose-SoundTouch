export function clampBalance(value, min, max) {
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || !Number.isSafeInteger(min) || !Number.isSafeInteger(max) || min > max) {
        return null;
    }
    return Math.max(min, Math.min(max, Math.round(numeric)));
}

export function balanceControlState(device) {
    const balance = device?.status?.balance;
    const min = balance?.balanceMin;
    const max = balance?.balanceMax;
    const defaultValue = balance?.balanceDefault;
    const capabilityKnown = balance?.capabilityKnown === true &&
        Number.isSafeInteger(min) && Number.isSafeInteger(max) && min <= max &&
        Number.isSafeInteger(defaultValue) && defaultValue >= min && defaultValue <= max;
    const available = capabilityKnown && balance?.balanceAvailable === true;
    const actual = balance?.actualBalance;
    const actualKnown = available && Number.isSafeInteger(actual) && actual >= min && actual <= max;
    const revision = Number.isSafeInteger(device?.status?.balanceRevision)
        ? device.status.balanceRevision
        : 0;

    return {
        enabled: Boolean(device?.status?.isConnected) && available && actualKnown,
        available,
        min: capabilityKnown ? min : null,
        max: capabilityKnown ? max : null,
        defaultValue: capabilityKnown ? defaultValue : null,
        value: actualKnown ? actual : null,
        revision,
    };
}

export function confirmedBalanceReadback(response, requested, min, max, afterRevision = -1) {
    const data = response?.data;
    if (!response?.success || data?.requested !== requested || !Number.isFinite(data?.actual) ||
        !Number.isSafeInteger(data?.revision) || data.revision <= afterRevision) {
        return null;
    }

    const actual = clampBalance(data.actual, min, max);
    return actual === data.actual ? { actual, revision: data.revision } : null;
}

export function confirmedBalanceActual(response, requested, min, max, afterRevision = -1) {
    return confirmedBalanceReadback(response, requested, min, max, afterRevision)?.actual ?? null;
}

export function balanceFailureMessage(response) {
    if (!response?.success) return response?.error || 'Stereo balance update failed.';
    if (response?.data?.atTarget === false) {
        return `Speaker reported ${response.data.actual}; requested ${response.data.requested}.`;
    }
    return '';
}
