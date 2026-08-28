import assert from 'node:assert/strict';
import test from 'node:test';

import { settingsSections } from '../static/js/settingsPresentation.mjs';

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
