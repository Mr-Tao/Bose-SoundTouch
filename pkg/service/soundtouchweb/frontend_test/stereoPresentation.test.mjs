import assert from 'node:assert/strict';
import test from 'node:test';

import { isSoundTouch10StereoPair } from '../static/js/stereoPresentation.mjs';

test('identifies only projected SoundTouch 10 stereo pairs', () => {
    assert.equal(isSoundTouch10StereoPair({
        info: { type: 'SoundTouch 10' },
        stereoPair: { masterDeviceId: 'left' },
    }), true);
    assert.equal(isSoundTouch10StereoPair({
        info: { type: 'ST10' },
        stereoPair: { masterDeviceId: 'left' },
    }), true);
    assert.equal(isSoundTouch10StereoPair({ info: { type: 'SoundTouch 10' } }), false);
    assert.equal(isSoundTouch10StereoPair({
        info: { type: 'SoundTouch 20' },
        stereoPair: { masterDeviceId: 'left' },
    }), false);
});
