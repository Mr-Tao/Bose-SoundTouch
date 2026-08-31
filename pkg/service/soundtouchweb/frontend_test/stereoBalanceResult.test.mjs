import assert from 'node:assert/strict';
import test from 'node:test';

import {
    balanceControlState,
    balanceFailureMessage,
    clampBalance,
    confirmedBalanceActual,
    confirmedBalanceReadback,
} from '../static/js/stereoBalanceResult.mjs';

test('clamps stereo balance to the confirmed speaker range', () => {
    assert.equal(clampBalance(-80, -7, 7), -7);
    assert.equal(clampBalance(80, -12, 9), 9);
    assert.equal(clampBalance(3.6, -7, 7), 4);
    assert.equal(clampBalance('not-a-number', -7, 7), null);
    assert.equal(clampBalance(0, 7, -7), null);
});

test('accepts only the requested confirmed balance readback', () => {
    assert.equal(confirmedBalanceActual({
        success: true,
        data: { requested: 20, target: 20, actual: 18, revision: 8, atTarget: false },
    }, 20, -30, 30, 7), 18);
    assert.deepEqual(confirmedBalanceReadback({
        success: true,
        data: { requested: 20, target: 20, actual: 18, revision: 8, atTarget: false },
    }, 20, -30, 30, 7), { actual: 18, revision: 8 });
    assert.equal(confirmedBalanceActual({
        success: true,
        data: { requested: 10, target: 10, actual: 10, revision: 8, atTarget: true },
    }, 20, -30, 30, 7), null);
    assert.equal(confirmedBalanceActual({
        success: true,
        data: { requested: 20, target: 20, actual: 31, revision: 8, atTarget: false },
    }, 20, -30, 30, 7), null);
    assert.equal(confirmedBalanceActual({
        success: true,
        data: { requested: 20, target: 20, actual: 18, revision: 7, atTarget: false },
    }, 20, -30, 30, 7), null);
    assert.equal(confirmedBalanceActual({ success: false, data: { requested: 20 } }, 20, -30, 30, 7), null);
});

for (const [name, extra] of [
    ['standalone stereo', {}],
    ['stereo master zone', { zone: { masterControlId: 'left' } }],
]) {
    test(`${name} keeps balance unknown until an available finite readback`, () => {
        const device = {
            ...extra,
            stereoPair: { masterDeviceId: 'LEFT' },
            status: { isConnected: true },
        };

        assert.deepEqual(balanceControlState(device), {
            enabled: false,
            available: false,
            min: null,
            max: null,
            defaultValue: null,
            value: null,
            revision: 0,
        });

        device.status.balanceRevision = 5;
        device.status.balance = {
            capabilityKnown: true,
            balanceAvailable: true,
            balanceMin: -7,
            balanceMax: 7,
            balanceDefault: 0,
            targetBalance: -3,
            actualBalance: -3,
        };
        assert.deepEqual(balanceControlState(device), {
            enabled: true,
            available: true,
            min: -7,
            max: 7,
            defaultValue: 0,
            value: -3,
            revision: 5,
        });
    });
}

test('unavailable and malformed capabilities cannot enable the control', () => {
    const device = {
        stereoPair: {},
        status: {
            isConnected: true,
            balanceRevision: 11,
            balance: {
                capabilityKnown: true,
                balanceAvailable: false,
                balanceMin: -7,
                balanceMax: 7,
                balanceDefault: 0,
                actualBalance: 0,
            },
        },
    };
    assert.equal(balanceControlState(device).enabled, false);
    assert.equal(balanceControlState(device).value, null);

    device.status.balance = {
        capabilityKnown: true,
        balanceAvailable: true,
        balanceMin: 9,
        balanceMax: -12,
        balanceDefault: 0,
        actualBalance: 0,
    };
    assert.equal(balanceControlState(device).enabled, false);
    assert.equal(balanceControlState(device).value, null);
});

test('supports a different valid advertised range', () => {
    const state = balanceControlState({
        stereoPair: {},
        status: {
            isConnected: true,
            balanceRevision: 11,
            balance: {
                capabilityKnown: true,
                balanceAvailable: true,
                balanceMin: -12,
                balanceMax: 9,
                balanceDefault: 1,
                actualBalance: 8,
            },
        },
    });
    assert.equal(state.enabled, true);
    assert.equal(state.revision, 11);
    assert.deepEqual([state.min, state.max, state.defaultValue, state.value], [-12, 9, 1, 8]);
});

test('distinguishes verified mismatch from gateway failure', () => {
    assert.equal(balanceFailureMessage({
        success: true,
        data: { requested: -10, target: -10, actual: -7, atTarget: false },
    }), 'Speaker reported -7; requested -10.');
    assert.equal(balanceFailureMessage({ success: false, error: 'readback is unverified' }),
        'readback is unverified');
    assert.equal(balanceFailureMessage({ success: true, data: { atTarget: true } }), '');
});
