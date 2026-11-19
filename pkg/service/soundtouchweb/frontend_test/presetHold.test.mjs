import assert from 'node:assert/strict';
import test from 'node:test';
import { createPresetHoldGesture, PRESET_HOLD_INTENT_MS, PRESET_HOLD_FILL_MS } from '../static/js/presetHold.mjs';

function setup(t) {
    t.mock.timers.enable({ apis: ['setTimeout'] });
    let now = 0;
    const actions = [];
    const rect = { left: 0, top: 0, right: 100, bottom: 50 };
    const event = {
        button: 0, pointerId: 1, isPrimary: true, clientX: 25, clientY: 25,
        currentTarget: {
            isConnected: true, closest: () => null, getClientRects: () => [rect], getBoundingClientRect: () => rect,
            setPointerCapture: () => {}, hasPointerCapture: () => false,
        },
    };
    const gesture = createPresetHoldGesture({
        window: { performance: { now: () => now } },
        getSnapshot: () => ({ owner: 'speaker', identity: 'station', current: {}, canTap: true, canHold: true }),
        onChange: () => {},
        onTap: slot => actions.push(['tap', slot]),
        onSave: slot => actions.push(['save', slot]),
    });
    return { gesture, event, actions, setTime: value => { now = value; } };
}

test('release after elapsed hold time cannot save before delayed progress has filled', t => {
    const { gesture, event, actions, setTime } = setup(t);
    gesture.down(event, 1);
    setTime(700);
    t.mock.timers.tick(PRESET_HOLD_INTENT_MS);
    setTime(PRESET_HOLD_INTENT_MS + PRESET_HOLD_FILL_MS + 40);
    gesture.up(event);
    gesture.click({ detail: 1 }, 1);
    assert.deepEqual(actions, []);
});

test('release outside the movement tolerance cancels even if pointermove was skipped', t => {
    const { gesture, event, actions } = setup(t);
    gesture.down(event, 1);
    t.mock.timers.tick(PRESET_HOLD_INTENT_MS);
    t.mock.timers.tick(PRESET_HOLD_FILL_MS);
    gesture.up({ ...event, clientX: 41 });
    gesture.click({ detail: 1 }, 1);
    assert.deepEqual(actions, []);
});

test('keyboard activation cancels the pointer gesture without re-enabling its click', t => {
    const { gesture, event, actions } = setup(t);
    gesture.down(event, 1);
    gesture.click({ detail: 0 }, 2);
    gesture.up(event);
    gesture.click({ detail: 1 }, 1);
    assert.deepEqual(actions, [['tap', 2]]);
});
