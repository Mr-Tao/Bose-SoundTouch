import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const app = await readFile(new URL('../static/js/app.js', import.meta.url), 'utf8');

test('explains the documented AirPlay limitation only for ST10 stereo pairs', () => {
    assert.match(app, /isSoundTouch10StereoPair\(device\)/);
    assert.match(app, /role="note" aria-label="Stereo pair limitation"/);
    assert.match(app, /AirPlay unavailable while paired\./);
    assert.match(app, /Unpair them to use AirPlay\./);
});
