import assert from 'node:assert/strict';
import test from 'node:test';
import { createPresetMutations, currentPresetContent, presetBank, presetContent, presetOutcomeMatches } from '../static/js/presetMutations.mjs';

const station = (id, name = id) => ({ Source: 'TUNEIN', Location: id, ItemName: name });
const initial = () => [station('A'), null, station('B'), null, null, null];

test('verification rejects duplicate, out-of-range and incomplete occupied slots', () => {
    for (const Preset of [
        [{ ID: 1, ContentItem: station('A') }, { ID: 1, ContentItem: station('B') }],
        [{ ID: 7, ContentItem: station('A') }],
        [{ ID: 1, ContentItem: { Source: 'TUNEIN', Location: '' } }],
    ]) assert.throws(() => presetBank({ presets: { Preset } }), /invalid preset bank/);
    assert.deepEqual(presetBank({ presets: { Preset: [
        { ID: 0, ContentItem: { Source: 'INVALID_SOURCE' } }, { ID: 1, ContentItem: null },
    ] } }), Array(6).fill(null));
});
function fixture() {
    const state = { bank: initial(), writes: [], reads: 0, views: [], current: true, failReads: false };
    const api = {
        async devicePresets() {
            state.reads++;
            if (state.failReads) throw new Error('unavailable');
            if (state.beforeRead) await state.beforeRead();
            return { success: true, data: { info: { device_id: 'physical' }, status: { epoch: 1, presets: { Preset:
                state.bank.flatMap((item, i) => item ? [{ ID: i + 1, ContentItem: item }] : []) } } } };
        },
        async storePresetContent(id, slot, item) {
            state.writes.push({ id, slot, item });
            if (state.apply !== false) {
                if (!state.copy) state.bank = state.bank.map(entry => entry?.Location === item.location ? null : entry);
                state.bank[slot - 1] = { Source: item.source, SourceAccount: item.sourceAccount, Location: item.location,
                    Type: item.type, ItemName: item.itemName, ContainerArt: item.containerArt };
            }
            if (state.afterWrite) state.afterWrite();
            return { success: true };
        },
        async removePreset(id, slot) { state.writes.push({ id, slot }); state.bank[slot - 1] = null; return { success: true }; },
    };
    state.controller = createPresetMutations({ api, deviceId: 'speaker', physicalId: 'physical', epoch: 1,
        isCurrent: () => state.current, onChange: value => { state.view = value; },
        onReadback: (...args) => state.views.push(args), delays: [0, 0, 0] });
    return state;
}

test('preflight warns of move and overwrite; cancel writes nothing', async () => {
    const f = fixture();
    await f.controller.request(3, station('A'));
    assert.equal(f.view.phase, 'confirm');
    assert.match(f.view.message, /Move “A” from preset 1 to preset 3/);
    assert.match(f.view.message, /Replace “B”/);
    assert.equal(f.writes.length, 0);
    f.controller.cancel();
    await f.controller.confirm();
    assert.equal(f.writes.length, 0);
});

test('double confirmation sends one frozen write and merges the full moved bank without events', async () => {
    const f = fixture();
    const item = station('A');
    await f.controller.request(3, item);
    item.Location = 'changed-playback';
    await Promise.all([f.controller.confirm(), f.controller.confirm()]);
    assert.equal(f.writes.length, 1);
    assert.equal(f.writes[0].item.location, 'A');
    assert.equal(f.view.phase, 'confirmed');
    const bank = presetBank(f.views.at(-1)[1]);
    assert.equal(bank[0], null);
    assert.equal(bank[2].location, 'A');
});

test('saving the same content to its current slot is a no-op, even with updated metadata', async () => {
    const f = fixture();
    await f.controller.request(1, station('A', 'new title'));
    await f.controller.confirm();
    assert.equal(f.view.phase, 'unchanged');
    assert.equal(f.writes.length, 0);
});

test('a changed bank requires a new confirmation and never writes against the old warning', async () => {
    const f = fixture();
    await f.controller.request(3, station('A'));
    f.bank[2] = station('C');
    await f.controller.confirm();
    assert.equal(f.writes.length, 0);
    assert.equal(f.view.phase, 'confirm');
    assert.match(f.view.message, /Presets changed.*Replace “C”/);
    await f.controller.confirm();
    assert.equal(f.writes.length, 1);
});

test('mismatch and failed readback stay unverified with exactly one write and three reads', async () => {
    for (const failure of ['mismatch', 'read']) {
        const f = fixture();
        f.apply = false;
        if (failure === 'read') f.afterWrite = () => { f.failReads = true; };
        await f.controller.request(3, station('A'));
        await f.controller.confirm();
        assert.equal(f.view.phase, 'unverified');
        assert.equal(f.writes.length, 1);
        assert.equal(f.reads, 5);
    }
});

test('pending preflight and pending confirmation cannot write after target change or disposal', async () => {
    for (const stage of ['preflight', 'confirm']) {
        const f = fixture();
        if (stage === 'confirm') await f.controller.request(3, station('A'));
        let release;
        f.beforeRead = () => new Promise(resolve => { release = resolve; });
        const pending = stage === 'confirm' ? f.controller.confirm() : f.controller.request(3, station('A'));
        f.current = false;
        f.controller.dispose();
        release();
        await pending;
        await f.controller.confirm();
        assert.equal(f.writes.length, 0);
    }
});

test('clear verifies absence and preservation of every other slot', async () => {
    const f = fixture();
    await f.controller.request(3, null);
    assert.match(f.view.message, /Clear preset 3: remove “B”/);
    await f.controller.confirm();
    assert.equal(f.view.phase, 'confirmed');
    assert.equal(f.bank[0].Location, 'A');
});

test('an unrelated slot changing prevents confirmation; firmware copy is allowed', () => {
    const before = initial().map(presetContent);
    const copy = [...before]; copy[2] = presetContent(station('A'));
    assert.equal(presetOutcomeMatches(before, copy, 3, copy[2]), true);
    copy[5] = presetContent(station('intruder'));
    assert.equal(presetOutcomeMatches(before, copy, 3, copy[2]), false);
});

test('normalized Orion locations, placeholder accounts and filled artwork confirm without changing frozen replay data', () => {
    const absolute = 'http://unifi/core02/svc-bmx-adapter-orion/prod/orion/station?data=abc';
    const before = Array(6).fill(null);
    before[0] = presetContent({ Source: 'LOCAL_INTERNET_RADIO', SourceAccount: 'LOCAL_INTERNET_RADIO', Location: absolute, ItemName: 'Plus' });
    const requested = { ...before[0] };
    const after = [...before];
    after[0] = { ...requested, sourceAccount: '', location: '/station?data=abc', containerArt: 'filled.png' };
    after[2] = { ...after[0] };
    assert.equal(presetOutcomeMatches(before, after, 3, requested), true);
    assert.equal(requested.location, absolute);
    after[2].location = '/station?data=different';
    assert.equal(presetOutcomeMatches(before, after, 3, requested), false);
});

test('requested rename must match and unrelated station metadata changes remain visible', () => {
    const before = initial().map(presetContent);
    const requested = { ...before[0], itemName: 'Renamed' };
    assert.equal(presetOutcomeMatches(before, before, 1, requested, { rename: true }), false);
    const after = [...before]; after[0] = requested;
    assert.equal(presetOutcomeMatches(before, after, 1, requested, { rename: true }), true);
    after[2] = { ...before[2], itemName: 'Unexpected name' };
    assert.equal(presetOutcomeMatches(before, after, 1, requested, { rename: true }), false);
});

test('current-save copies top-level artwork and refuses explicitly non-presetable content', () => {
    const playing = { Source: 'TUNEIN', ContentItem: { ...station('A'), IsPresetable: true }, Art: { URL: 'station.png', ArtImageStatus: 'IMAGE_PRESENT' } };
    assert.equal(currentPresetContent(playing).ContainerArt, 'station.png');
    assert.equal(playing.ContentItem.ContainerArt, undefined);
    playing.ContentItem.IsPresetable = false;
    assert.equal(currentPresetContent(playing), null);
    playing.ContentItem.IsPresetable = true;
    playing.Art.ArtImageStatus = 'INVALID';
    assert.equal(currentPresetContent(playing).ContainerArt, '');
});

test('explicit Move clears a verified old copy once, while automatic firmware move needs no DELETE', async () => {
    for (const copy of [false, true]) {
        const f = fixture(); f.copy = copy;
        await f.controller.request(3, station('A'), { moveFrom: 1 });
        assert.match(f.view.message, /Preset 1 will be cleared after the destination is verified/);
        await f.controller.confirm();
        assert.equal(f.view.phase, 'confirmed');
        assert.equal(f.writes.length, copy ? 2 : 1);
        if (copy) assert.deepEqual(f.writes[1], { id: 'speaker', slot: 1 });
        assert.equal(f.bank[0], null);
    }
});

test('explicit Move refuses changed source and cannot clear it after a mismatching destination', async () => {
    const f = fixture(); f.apply = false;
    await f.controller.request(3, station('A'), { moveFrom: 1 });
    await f.controller.confirm();
    assert.equal(f.view.phase, 'unverified');
    assert.equal(f.writes.length, 1);
    const stale = fixture();
    await stale.controller.request(3, station('A'), { moveFrom: 1 });
    stale.bank[0] = station('unexpected');
    await stale.controller.confirm();
    assert.equal(stale.view.phase, 'failed');
    assert.equal(stale.writes.length, 0);
});

test('explicit Move into an existing matching copy only clears the disclosed source', async () => {
    const f = fixture();
    f.bank[2] = station('A');
    await f.controller.request(3, station('A'), { moveFrom: 1 });
    await f.controller.confirm();
    assert.equal(f.view.phase, 'confirmed');
    assert.deepEqual(f.writes, [{ id: 'speaker', slot: 1 }]);
});

test('unnamed confirmations do not expose opaque replay URLs', async () => {
    const f = fixture();
    await f.controller.request(3, station('https://private.example/opaque?token=secret', ''));
    assert.match(f.view.message, /Unnamed station/);
    assert.doesNotMatch(f.view.message, /private.example|secret/);
});
