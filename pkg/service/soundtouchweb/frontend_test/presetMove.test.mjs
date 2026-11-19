import assert from 'node:assert/strict';
import test from 'node:test';
import { movePreset, saveGridPreset, verifyPresetEdit } from '../static/js/presetMove.mjs';

const source = { Source: 'TUNEIN', Location: 's12345', ItemName: 'WDR 2' };
const preset = (ID, ContentItem = source) => ({ ID, ContentItem });

function setup({ afterStore, afterDelete, before = [preset(1)] }) {
    let bank = before;
    const calls = [];
    const options = {
        deviceId: 'speaker', physicalId: 'DEVICE', epoch: 1, from: 1, to: 3, source,
        isCurrent: () => true,
        api: {
            async devicePresets(_, { headers }) {
                assert.equal(headers['X-AfterTouch-Preset-Target'], 'DEVICE');
                assert.equal(headers['X-AfterTouch-Preset-Epoch'], '1');
                calls.push('read');
                return { success: true, data: { info: { device_id: 'DEVICE' }, status: { epoch: 1, presets: { Preset: bank } } } };
            },
            async storePresetContent(_, slot) {
                calls.push(`write ${slot}`);
                if (afterStore) bank = afterStore;
                return { success: true };
            },
            async removePreset(_, slot) {
                calls.push(`clear ${slot}`);
                if (afterDelete) bank = afterDelete;
                return { success: true };
            },
        },
    };
    return { options, calls };
}

test('an acknowledged store without destination evidence never deletes the original preset', async () => {
    const { options, calls } = setup({});
    await assert.rejects(movePreset(options), /did not clear the original slot/);
    assert.deepEqual(calls, ['read', 'write 3', 'read', 'read', 'read']);
});

test('a contradictory destination readback cannot authorize deletion', async () => {
    const { options, calls } = setup({ afterStore: [preset(1), preset(3, null), preset(3)] });
    await assert.rejects(movePreset(options), /did not clear the original slot/);
    assert.deepEqual(calls, ['read', 'write 3', 'read', 'read', 'read']);
});

test('a destination that changed since opening requires a new confirmation', async () => {
    const { options, calls } = setup({ before: [preset(1), preset(3, { ...source, Location: 's4321' })] });
    await assert.rejects(movePreset(options), /source or destination preset changed/);
    assert.deepEqual(calls, ['read']);
});

test('a firmware move needs no second write when the original slot is already empty', async () => {
    const { options, calls } = setup({ afterStore: [preset(0, null), preset(1, null), preset(3)] });
    await movePreset(options);
    assert.deepEqual(calls, ['read', 'write 3', 'read']);
});

test('an acknowledged delete without source-clear evidence leaves the move unverified', async () => {
    const { options, calls } = setup({ afterStore: [preset(1), preset(3)] });
    await assert.rejects(movePreset(options), /clearing the original slot is unverified/);
    assert.deepEqual(calls, ['read', 'write 3', 'read', 'clear 1', 'read', 'read', 'read']);
});

test('an acknowledged save or clear without a changed preset is not confirmed', async () => {
    for (const { slot, content, expected, write } of [
        { slot: 3, content: source, expected: null, write: 'write 3' },
        { slot: 1, content: null, expected: source, write: 'clear 1' },
    ]) {
        const { options, calls } = setup({});
        await assert.rejects(verifyPresetEdit({ ...options, slot, content, expected }), /Preset change unverified/);
        assert.deepEqual(calls, ['read', write, 'read', 'read', 'read']);
    }
});

test('a lost write response can confirm once the speaker reports the selected content', async () => {
    const { options, calls } = setup({ afterStore: [preset(1), preset(3)] });
    const store = options.api.storePresetContent;
    options.api.storePresetContent = async (...args) => { await store(...args); throw new Error('response lost'); };
    await verifyPresetEdit({ ...options, slot: 3, content: source, expected: null });
    assert.deepEqual(calls, ['read', 'write 3', 'read']);
});

test('losing a differently named preset for the same station prevents confirmation', async () => {
    const another = { ...source, ItemName: 'WDR 2 alternate' };
    const { options, calls } = setup({ before: [preset(1, another)], afterStore: [preset(3)] });
    await assert.rejects(verifyPresetEdit({ ...options, slot: 3, content: source, expected: null }), /Preset change unverified/);
    assert.deepEqual(calls, ['read', 'write 3', 'read', 'read', 'read']);
});

test('a released hold on an empty slot saves only after two matching fresh reads', async () => {
    const { options, calls } = setup({ before: [], afterStore: [preset(3)] });
    assert.equal(await saveGridPreset({ ...options, slot: 3, content: source, hold: true }), null);
    assert.deepEqual(calls, ['read', 'read', 'write 3', 'read']);
});

test('a changed bank after release or after confirmation requires fresh approval', async () => {
    const other = preset(2, { ...source, Location: 's54321', ItemName: 'WDR 4' });
    const { options, calls } = setup({ before: [] });
    const read = options.api.devicePresets;
    let stage = 0;
    options.api.devicePresets = async (...args) => {
        const response = await read(...args);
        response.data.status.presets.Preset = [[], [other], [other, preset(1)]][stage++];
        return response;
    };
    const request = { ...options, slot: 3, content: source, hold: true };
    const revised = await saveGridPreset(request);
    assert.equal(revised.bank[2].itemName, 'WDR 4');
    assert.deepEqual(revised.duplicates, []);
    const latest = await saveGridPreset({ ...request, approvedBank: revised.bank });
    assert.deepEqual(latest.duplicates, [1]);
    assert.deepEqual(calls, ['read', 'read', 'read']);
});

test('a confirmed duplicate can be removed by the speaker without an additional clear', async () => {
    const { options, calls } = setup({ afterStore: [preset(3)] });
    const request = { ...options, slot: 3, content: source, hold: true };
    const prompt = await saveGridPreset(request);
    assert.deepEqual(prompt.duplicates, [1]);
    assert.deepEqual(calls, ['read']);
    assert.equal(await saveGridPreset({ ...request, approvedBank: prompt.bank }), null);
    assert.deepEqual(calls, ['read', 'read', 'write 3', 'read']);
});
