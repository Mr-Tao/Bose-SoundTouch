import assert from 'node:assert/strict';
import test from 'node:test';

import { bassControlForStatus } from '../static/js/bassCapabilities.mjs';

test('uses the reported SoundTouch -9 to 0 capability', () => {
    assert.deepEqual(bassControlForStatus({
        bass: { TargetBass: -2, ActualBass: -3 },
        bassCapabilities: {
            BassAvailable: true,
            BassMin: -9,
            BassMax: 0,
            BassDefault: 0,
        },
    }), { available: true, min: -9, max: 0, defaultValue: 0, value: -3 });
});

test('uses a different valid reported range and default', () => {
    assert.deepEqual(bassControlForStatus({
        bass: { TargetBass: 10, ActualBass: 12 },
        bassCapabilities: {
            BassAvailable: true,
            BassMin: -4,
            BassMax: 12,
            BassDefault: 1,
        },
    }), { available: true, min: -4, max: 12, defaultValue: 1, value: 12 });
});

test('hides bass when the device reports it unavailable', () => {
    assert.deepEqual(bassControlForStatus({
        bass: { TargetBass: -1, ActualBass: 0 },
        bassCapabilities: {
            BassAvailable: false,
            BassMin: 0,
            BassMax: 0,
            BassDefault: 0,
        },
    }), { available: false });
});

test('uses only the conservative fallback while capabilities are unknown', () => {
    assert.deepEqual(bassControlForStatus({ bass: { TargetBass: -1, ActualBass: -2 } }), {
        available: true,
        min: -9,
        max: 0,
        defaultValue: 0,
        value: -2,
    });
    assert.deepEqual(bassControlForStatus({}), { available: false });
});

test('treats incomplete or invalid capabilities as unknown', () => {
    for (const bassCapabilities of [
        { BassAvailable: true, BassMin: -9, BassDefault: 0 },
        { BassAvailable: false, BassMin: 0, BassDefault: 0 },
        { BassAvailable: true, BassMin: 1, BassMax: 0, BassDefault: 0 },
    ]) {
        assert.deepEqual(bassControlForStatus({
            bass: { TargetBass: -1, ActualBass: -2 },
            bassCapabilities,
        }), {
            available: true,
            min: -9,
            max: 0,
            defaultValue: 0,
            value: -2,
        });
    }
});
