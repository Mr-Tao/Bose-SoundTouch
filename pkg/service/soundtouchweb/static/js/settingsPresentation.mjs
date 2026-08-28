export const SETTINGS_SECTION_ORDER = [
    'clock',
    'standby',
    'language',
    'sync',
    'bluetooth',
    'sources',
    'network',
];

function hasError(errors, keys) {
    if (!errors || typeof errors !== 'object') return false;
    return keys.some(key => Boolean(errors[key]));
}

export function settingsSections(snapshot) {
    const support = snapshot?.support || {};
    const errors = snapshot?.errors;
    const visible = {
        clock: Boolean(support.clockDisplay || support.clockTime),
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
