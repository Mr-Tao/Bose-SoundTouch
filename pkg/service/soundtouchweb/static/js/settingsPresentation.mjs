export const SETTINGS_SECTION_ORDER = [
    'clock',
    'standby',
    'language',
    'sync',
    'bluetooth',
    'sources',
    'network',
];

export function deviceSettingsTitle(name) {
    const target = typeof name === 'string' ? name.trim() : '';
    return target ? `Device settings · ${target}` : 'Device settings';
}

function hasError(errors, keys) {
    if (!errors || typeof errors !== 'object') return false;
    return keys.some(key => Boolean(errors[key]));
}

function hasOwn(value, key) {
    return value != null && Object.prototype.hasOwnProperty.call(value, key);
}

export function clockControls(snapshot) {
    const support = snapshot?.support || {};
    const display = snapshot?.clockDisplay;
    const time = snapshot?.clockTime;
    const displayReadback = Boolean(support.clockDisplay && display);

    return {
        display: displayReadback && hasOwn(display, 'enabled') && typeof display.enabled === 'boolean',
        format: displayReadback && ['12', '24', 'auto'].includes(display.format),
        timeZone: displayReadback && typeof display.timeZone === 'string' && display.timeZone.trim() !== '',
        currentTime: Boolean(
            support.clockTime && time && (
                (Number.isFinite(time.utc) && time.utc > 0)
                || (typeof time.value === 'string' && time.value.trim() !== '')
            ),
        ),
    };
}

export function clockDisplayPatch(snapshot, field, value) {
    const controls = clockControls(snapshot);

    if (field === 'enabled' && controls.display && typeof value === 'boolean') {
        return { enabled: value };
    }
    if (field === 'format' && controls.format && ['12', '24', 'auto'].includes(value)) {
        return { format: value };
    }
    if (field === 'timeZone' && controls.timeZone && typeof value === 'string' && value.trim() !== '') {
        return { timeZone: value.trim() };
    }

    return null;
}

export function settingsSections(snapshot) {
    const support = snapshot?.support || {};
    const errors = snapshot?.errors;
    const clock = clockControls(snapshot);
    const visible = {
        clock: Object.values(clock).some(Boolean) || hasError(errors, ['clock', 'clockDisplay', 'clockTime']),
        standby: Boolean(support.systemTimeout),
        language: Boolean(support.language),
        sync: Boolean(support.sync),
        bluetooth: Boolean(
            support.bluetooth || support.bluetoothPair || support.bluetoothClear,
        ),
        sources: Boolean(support.sourceNaming) || hasError(errors, ['sources', 'sourceNaming']),
        network: snapshot?.network != null || Boolean(
            support.wifiOnboarding && snapshot?.onboardingUrl,
        ) || hasError(errors, ['network', 'wifiOnboarding']),
    };

    return SETTINGS_SECTION_ORDER.filter(section => visible[section]);
}
