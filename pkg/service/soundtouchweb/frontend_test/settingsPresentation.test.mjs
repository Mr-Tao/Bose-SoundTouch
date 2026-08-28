import assert from 'node:assert/strict';
import test from 'node:test';

import {
    clockControls,
    clockDisplayPatch,
    deviceSettingsTitle,
    firmwareNetworkQualityEvidence,
    networkInterfaceGroups,
    networkInterfaceName,
    networkInterfaceSummary,
    settingsSections,
} from '../static/js/settingsPresentation.mjs';

test('device settings title identifies its physical target', () => {
    assert.equal(deviceSettingsTitle(' kuchyň '), 'Device settings · kuchyň');
    assert.equal(deviceSettingsTitle(''), 'Device settings');
});

test('settings sections follow the stable presentation order', () => {
    assert.deepEqual(settingsSections({
        support: {
            wifiOnboarding: true,
            sourceNaming: true,
            sync: true,
            bluetoothClear: true,
            language: true,
            systemTimeout: true,
            clockTime: true,
        },
        clockTime: { utc: 1787882400 },
        onboardingUrl: 'http://192.0.2.10/setup',
    }), [
        'clock',
        'standby',
        'language',
        'sync',
        'bluetooth',
        'sources',
        'network',
    ]);
});

test('ST20 clock readback exposes each supported control', () => {
    const snapshot = {
        support: { clockDisplay: true, clockTime: true },
        clockDisplay: {
            enabled: false,
            format: '24',
            timeZone: 'Europe/Prague',
        },
        clockTime: { utc: 1787882400 },
    };

    assert.deepEqual(clockControls(snapshot), {
        display: true,
        format: true,
        timeZone: true,
        currentTime: true,
    });
    assert.deepEqual(clockDisplayPatch(snapshot, 'enabled', true), { enabled: true });
    assert.deepEqual(clockDisplayPatch(snapshot, 'format', '12'), { format: '12' });
    assert.deepEqual(clockDisplayPatch(snapshot, 'timeZone', ' Europe/Berlin '), {
        timeZone: 'Europe/Berlin',
    });
});

test('omitted clock fields do not become controls or writable defaults', () => {
    const snapshot = {
        support: { clockDisplay: true, clockTime: true },
        clockDisplay: { enabled: false },
        clockTime: {},
    };

    assert.deepEqual(clockControls(snapshot), {
        display: true,
        format: false,
        timeZone: false,
        currentTime: false,
    });
    assert.equal(clockDisplayPatch(snapshot, 'format', '24'), null);
    assert.equal(clockDisplayPatch(snapshot, 'timeZone', 'Europe/Prague'), null);
    assert.deepEqual(settingsSections(snapshot), ['clock']);
});

test('clock display toggle requires enabled readback', () => {
    const snapshot = {
        support: { clockDisplay: true },
        clockDisplay: { format: '24', timeZone: 'Europe/Prague' },
    };

    assert.deepEqual(clockControls(snapshot), {
        display: false,
        format: true,
        timeZone: true,
        currentTime: false,
    });
    assert.equal(clockDisplayPatch(snapshot, 'enabled', true), null);
});

test('ST10 clock omission exposes no clock settings', () => {
    const snapshot = { support: {} };

    assert.deepEqual(clockControls(snapshot), {
        display: false,
        format: false,
        timeZone: false,
        currentTime: false,
    });
    assert.deepEqual(settingsSections(snapshot), []);
    assert.equal(clockDisplayPatch(snapshot, 'enabled', true), null);
});

test('unsupported setting data does not expose mutation controls', () => {
    assert.deepEqual(settingsSections({
        support: {},
        clockDisplay: { enabled: true },
        language: { code: 3 },
        sources: [{ source: 'AUX', displayName: 'Auxiliary' }],
    }), []);
});

test('network status is visible without Wi-Fi onboarding support', () => {
    assert.deepEqual(settingsSections({
        support: {},
        network: { interfaces: [] },
    }), ['network']);
});

test('network presentation separates connected interfaces from disconnected interfaces', () => {
    const network = {
        interfaces: [
            {
                type: 'WIFI_INTERFACE',
                name: 'wlan0',
                ipAddress: '192.0.2.10',
                ssid: 'Test WiFi',
                band: '5GHz',
                state: 'NETWORK_WIFI_CONNECTED',
                firmwareNetworkQuality: 'Marginal',
                firmwareNetworkQualitySource: 'netStats',
                firmwareNetworkQualityState: 'reported',
            },
            {
                type: 'WIFI_INTERFACE',
                name: 'wlan1',
                macAddress: 'AA:BB:CC:DD:EE:01',
                ipAddress: '192.0.2.11',
                band: '2.4GHz',
                state: 'NETWORK_WIFI_DISCONNECTED',
            },
            {
                type: 'ETHERNET_INTERFACE',
                name: 'eth0',
                macAddress: 'AA:BB:CC:DD:EE:02',
                state: 'NETWORK_ETHERNET_DISCONNECTED',
            },
        ],
    };

    const groups = networkInterfaceGroups(network);
    assert.equal(groups.connected.length, 1);
    assert.equal(groups.disconnected.length, 2);
    assert.equal(networkInterfaceName(groups.connected[0]), 'Wi-Fi · Test WiFi');
    assert.equal(
        networkInterfaceSummary(groups.connected[0]),
        '192.0.2.10 · 5GHz',
    );
    assert.equal(networkInterfaceName(groups.disconnected[0], true), 'Wi-Fi interface · wlan1');
    assert.equal(
        networkInterfaceSummary(groups.disconnected[0], true),
        'Disconnected · 192.0.2.11 · 2.4GHz',
    );
});

test('firmware network quality remains subordinate topology-sensitive evidence', () => {
    const summary = networkInterfaceSummary({ ipAddress: '192.0.2.10', band: '5GHz' });
    const evidence = firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: 'Good',
        firmwareNetworkQualitySource: 'netStats',
        firmwareNetworkQualityState: 'reported',
    });

    assert.equal(summary, '192.0.2.10 · 5GHz');
    assert.equal(evidence, 'Firmware network quality: Good (/netStats; topology-sensitive)');
    assert.equal(`${summary} ${evidence}`.includes('dBm'), false);
    assert.equal(`${summary} ${evidence}`.includes('RSSI'), false);
    assert.equal(`${summary} ${evidence}`.includes('Signal'), false);
});

test('firmware network quality conflict retains both endpoint values', () => {
    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: 'Good',
        firmwareNetworkQualitySource: 'netStats',
        firmwareNetworkQualityState: 'conflict',
        networkInfoFirmwareQuality: 'Poor',
    }), 'Firmware network quality: Good (/netStats; /networkInfo: Poor; topology-sensitive)');
});

test('Fair and Marginal remain distinct known firmware quality categories', () => {
    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: 'Fair',
        firmwareNetworkQualitySource: 'networkInfo',
        firmwareNetworkQualityState: 'reported',
    }), 'Firmware network quality: Fair (/networkInfo; topology-sensitive)');
    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: 'Marginal',
        firmwareNetworkQualitySource: 'netStats',
        firmwareNetworkQualityState: 'reported',
    }), 'Firmware network quality: Marginal (/netStats; topology-sensitive)');
});

test('firmware network quality fallback and unavailable states are explicit', () => {
    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: 'Poor',
        firmwareNetworkQualitySource: 'networkInfo',
        firmwareNetworkQualityState: 'fallback',
    }), 'Firmware network quality: Poor (/networkInfo fallback; topology-sensitive)');

    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQualityState: 'unavailable',
    }), 'Firmware network quality unavailable (topology-sensitive telemetry)');
    assert.equal(firmwareNetworkQualityEvidence({
        firmwareNetworkQuality: '-54 dBm',
        firmwareNetworkQualityState: 'reported',
    }), 'Firmware network quality unavailable (topology-sensitive telemetry)');
});

test('Wi-Fi onboarding requires both capability and URL', () => {
    assert.deepEqual(settingsSections({
        support: { wifiOnboarding: true },
    }), []);

    assert.deepEqual(settingsSections({
        support: { wifiOnboarding: true },
        onboardingUrl: 'http://192.0.2.10/setup',
    }), ['network']);
});

test('partial source and network read errors remain visible in order', () => {
    assert.deepEqual(settingsSections({
        support: {},
        errors: {
            network: 'network read failed',
            sources: 'source read failed',
        },
    }), ['sources', 'network']);
});

test('expected network telemetry disagreement is not a red section error', () => {
    assert.deepEqual(settingsSections({
        support: {},
        errors: { networkStats: 'telemetry differs' },
    }), []);
});
