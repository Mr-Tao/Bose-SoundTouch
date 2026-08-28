import assert from 'node:assert/strict';
import test from 'node:test';

import {
    clockControls,
    clockDisplayPatch,
    settingsSections,
} from '../static/js/settingsPresentation.mjs';

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
