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

function networkInterfaces(network) {
    if (Array.isArray(network?.interfaces)) return network.interfaces;
    if (Array.isArray(network?.interfaces?.interfaces)) return network.interfaces.interfaces;
    return [];
}

function isConnectedNetworkInterface(item) {
    return item?.state === 'NETWORK_WIFI_CONNECTED'
        || item?.state === 'NETWORK_ETHERNET_CONNECTED';
}

export function networkInterfaceGroups(network) {
    const interfaces = networkInterfaces(network);
    return {
        connected: interfaces.filter(isConnectedNetworkInterface),
        disconnected: interfaces.filter(item => !isConnectedNetworkInterface(item)),
    };
}

export function networkInterfaceName(item, technical = false) {
    if (item?.type === 'WIFI_INTERFACE') {
        if (technical) return item.name ? `Wi-Fi interface · ${item.name}` : 'Wi-Fi interface';
        return item.ssid ? `Wi-Fi · ${item.ssid}` : 'Wi-Fi';
    }
    if (item?.type === 'ETHERNET_INTERFACE') {
        if (technical) return item.name ? `Ethernet interface · ${item.name}` : 'Ethernet interface';
        return 'Ethernet';
    }
    return item?.name || item?.type || 'Network interface';
}

export function networkInterfaceSummary(item, technical = false) {
    if (technical) {
        return [
            item?.state?.endsWith('_DISCONNECTED') ? 'Disconnected' : 'Not connected',
            item?.ipAddress,
            item?.band,
        ].filter(Boolean).join(' · ');
    }

    return [item?.ipAddress || 'IP not reported', item?.band]
        .filter(Boolean)
        .join(' · ');
}

export function firmwareNetworkQualityEvidence(item) {
    if (!item?.firmwareNetworkQuality && !item?.firmwareNetworkQualityState) return '';

    const validQuality = ['Excellent', 'Good', 'Fair', 'Marginal', 'Poor']
        .includes(item?.firmwareNetworkQuality)
        ? item.firmwareNetworkQuality
        : '';
    if (!validQuality || item?.firmwareNetworkQualityState === 'unavailable') {
        return 'Firmware network quality unavailable (topology-sensitive telemetry)';
    }

    let provenance = '';
    if (item?.firmwareNetworkQualityState === 'fallback') {
        provenance = '/networkInfo fallback';
    } else if (item?.firmwareNetworkQualityState === 'conflict') {
        const networkInfoQuality = ['Excellent', 'Good', 'Fair', 'Marginal', 'Poor']
            .includes(item?.networkInfoFirmwareQuality)
            ? item.networkInfoFirmwareQuality
            : 'different value';
        provenance = `/netStats; /networkInfo: ${networkInfoQuality}`;
    } else if (item?.firmwareNetworkQualitySource === 'netStats') {
        provenance = '/netStats';
    } else if (item?.firmwareNetworkQualitySource === 'networkInfo') {
        provenance = '/networkInfo';
    }

    const details = [provenance, 'topology-sensitive'].filter(Boolean).join('; ');
    return `Firmware network quality: ${validQuality}${details ? ` (${details})` : ''}`;
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
