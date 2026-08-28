import assert from 'node:assert/strict';
import test from 'node:test';

import {
    confirmedSettingActual,
    confirmedSettingReadback,
    deviceSoundTarget,
    formatBalanceValue,
    newerSettingProjection,
    resetValue,
    shouldClearSettingFailure,
    steppedValue,
    stereoPairPresentation,
    targetBassStatus,
} from '../static/js/soundSettingsPresentation.mjs';

test('device sound target remains physical while pair balance remains logical', () => {
    const target = {
        controlId: '192.0.2.11',
        deviceId: 'right-id',
        name: 'Right physical',
        connectivity: 'online',
        bass: { TargetBass: -2, ActualBass: -3 },
        bassRevision: 4,
    };
    const member = {
        name: 'Living pair',
        deviceSettingsTarget: target,
        stereoPair: { name: 'Living pair' },
        balance: { actualBalance: 0 },
        balanceRevision: 9,
        available: true,
    };

    assert.equal(deviceSoundTarget(null, member), target);
    assert.deepEqual(targetBassStatus(target), {
        bass: target.bass,
        bassCapabilities: undefined,
        bassRevision: 4,
        connectivity: 'online',
        isConnected: true,
    });
    const pair = stereoPairPresentation('pair-control', null, member);
    assert.equal(pair.controlId, 'pair-control');
    assert.equal(pair.name, 'Living pair');
    assert.equal(pair.device.status.balanceRevision, 9);
});

test('one-step and reset helpers reject no-op and invalid writes', () => {
    assert.equal(steppedValue(-3, -1, -9, 0), -4);
    assert.equal(steppedValue(-9, -1, -9, 0), null);
    assert.equal(steppedValue(0, 1, -9, 0), null);
    assert.equal(resetValue(-3, 0, -9, 0), 0);
    assert.equal(resetValue(0, 0, -9, 0), null);
    assert.equal(resetValue(0, 2, -9, 0), null);
});

test('balance labels state direction and center explicitly', () => {
    assert.equal(formatBalanceValue(-4), '+4 Left');
    assert.equal(formatBalanceValue(0), 'Centered');
    assert.equal(formatBalanceValue(3), '+3 Right');
});

test('authoritative setting readback validates request identity and range', () => {
    assert.equal(confirmedSettingActual({
        success: true,
        data: { requested: -3, target: -3, actual: -3, revision: 8, atTarget: true },
    }, -3, -9, 0, 7), -3);
    assert.deepEqual(confirmedSettingReadback({
        success: true,
        data: { requested: -3, target: -2, actual: -4, revision: 9, atTarget: false },
    }, -3, -9, 0, 7), { actual: -4, revision: 9 });
    assert.equal(confirmedSettingActual({
        success: true,
        data: { requested: -2, target: -2, actual: -2, revision: 8, atTarget: true },
    }, -3, -9, 0, 7), null);
    assert.equal(confirmedSettingActual({
        success: true,
        data: { requested: -3, target: 1, actual: 1, revision: 8, atTarget: false },
    }, -3, -9, 0, 7), null);
    assert.equal(confirmedSettingActual({
        success: true,
        data: { requested: -3, target: -3, actual: -3, revision: 7, atTarget: true },
    }, -3, -9, 0, 7), null);
});

test('setting failures clear only after a newer authoritative revision', () => {
    const state = {
        failedRevision: 8,
        projectionRevision: 9,
        available: true,
        projectedValue: -3,
        busy: false,
    };
    assert.equal(shouldClearSettingFailure(state), true);
    assert.equal(shouldClearSettingFailure({ ...state, projectionRevision: 8 }), false);
    assert.equal(shouldClearSettingFailure({ ...state, available: false }), false);
    assert.equal(shouldClearSettingFailure({ ...state, busy: true }), false);
});

test('a newer projection supersedes an older HTTP readback regardless of value', () => {
    assert.deepEqual(newerSettingProjection({
        projectionRevision: 12,
        projectedValue: -4,
        available: true,
        afterRevision: 11,
    }), { actual: -4, revision: 12 });
    assert.deepEqual(newerSettingProjection({
        projectionRevision: 12,
        projectedValue: -3,
        available: true,
        afterRevision: 11,
    }), { actual: -3, revision: 12 });
    assert.equal(newerSettingProjection({
        projectionRevision: 11,
        projectedValue: -4,
        available: true,
        afterRevision: 11,
    }), null);
    assert.equal(newerSettingProjection({
        projectionRevision: 12,
        projectedValue: -4,
        available: false,
        afterRevision: 11,
    }), null);
});
