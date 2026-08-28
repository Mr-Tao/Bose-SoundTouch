import assert from 'node:assert/strict';
import test from 'node:test';

import {
    shouldClearBalanceFailure,
} from '../static/js/components/StereoBalanceControl.js';

function reconciliation(overrides = {}) {
    return shouldClearBalanceFailure({
        failedRevision: 4,
        projectionRevision: 5,
        enabled: true,
        projectedBalance: -2,
        busy: false,
        ...overrides,
    });
}

test('clears a request failure after a newer valid idle projection', () => {
    assert.equal(reconciliation(), true);
});

test('preserves a request failure until a newer authoritative balance revision', () => {
    assert.equal(reconciliation({ projectionRevision: 4 }), false);
    assert.equal(reconciliation({ failedRevision: null }), false);
});

test('preserves a request failure for invalid or busy projections', () => {
    assert.equal(reconciliation({ enabled: false }), false);
    assert.equal(reconciliation({ projectedBalance: null }), false);
    assert.equal(reconciliation({ busy: true }), false);
});
