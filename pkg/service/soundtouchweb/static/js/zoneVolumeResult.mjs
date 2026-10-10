export function clampVolume(value) {
    return Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
}

export function maxReadbackActual(data) {
    const actuals = (data?.members || [])
        .filter(member => Number.isFinite(member.actual))
        .map(member => member.actual);
    return actuals.length > 0 ? clampVolume(Math.max(...actuals)) : null;
}

export function fenceVolumeReadback(capturedProjection, currentProjection,
    currentVolume, confirmedVolume) {
    const accepted = capturedProjection === currentProjection;
    return {
        accepted,
        volume: accepted ? confirmedVolume : currentVolume,
    };
}

export function partialFailureMessage(data) {
    const members = data?.members || [];
    const summaries = ['failed', 'unverified'].map(status => {
        const affected = members.filter(member =>
            member.status === status || (status === 'failed' && !member.status && member.error));
        if (affected.length === 0) return '';
        const names = affected.slice(0, 2)
            .map(member => member.name || member.controlId || member.deviceId);
        const remaining = affected.length - names.length;
        const suffix = remaining > 0 ? ` +${remaining}` : '';
        return `${affected.length} ${affected.length === 1 ? 'member' : 'members'} ${status}: ${names.join(', ')}${suffix}`;
    }).filter(Boolean);
    return summaries.join('; ') || (data?.partial ? 'Some member updates are unverified.' : '');
}
