import { canonicalLocation } from './contentLocation.mjs';

export function presetContent(item) {
    if (!item) return null;
    return {
        source: item.Source ?? item.source ?? '',
        sourceAccount: item.SourceAccount ?? item.sourceAccount ?? '',
        location: item.Location ?? item.location ?? '',
        type: item.Type ?? item.type ?? '',
        itemName: item.ItemName ?? item.itemName ?? '',
        containerArt: item.ContainerArt ?? item.containerArt ?? '',
    };
}

export function currentPresetContent(nowPlaying) {
    const item = nowPlaying?.ContentItem;
    if (nowPlaying?.Source === 'STANDBY' || !item?.Source || !item?.Location || item.IsPresetable === false) return null;
    const art = nowPlaying?.Art;
    return { ...item, ContainerArt: item.ContainerArt || (art?.ArtImageStatus === 'IMAGE_PRESENT' ? art.URL : '') || '' };
}

function identity(item) {
    const content = presetContent(item);
    if (!content) return '';
    return JSON.stringify([content.source,
        content.sourceAccount === content.source ? '' : content.sourceAccount,
        canonicalLocation(content.source, content.location)]);
}

export function presetBank(status) {
    const bank = Array(6).fill(null);
    const occupied = new Set();
    for (const preset of status?.presets?.Preset ?? []) {
        const content = presetContent(preset.ContentItem);
        // Firmware can include empty placeholders, including slot zero.
        if (!content || (content.source === 'INVALID_SOURCE' && !content.location)) continue;
        const slot = Number(preset.ID);
        if (!Number.isInteger(slot) || slot < 1 || slot > 6 || occupied.has(slot) || !content.source || !content.location) {
            throw new Error('The speaker returned an invalid preset bank. Reopen the editor.');
        }
        occupied.add(slot);
        bank[slot - 1] = content;
    }
    return bank;
}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const name = item => item?.itemName || 'Unnamed station';

function equivalentContent(before, after, checkName = true) {
    if (!before || !after) return before === after;
    return identity(before) === identity(after) && (!checkName || before.itemName === after.itemName) &&
        (!before.type || before.type === after.type) && (!before.containerArt || before.containerArt === after.containerArt);
}

// AbortSignal.timeout is newer than the supported Safari baseline.
async function timedRequest(request) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 4000);
    try { return await request(controller.signal); }
    finally { clearTimeout(timer); }
}

export function presetWarning(bank, slot, item) {
    const target = bank[slot - 1];
    if (!item) return `Clear preset ${slot}: remove “${name(target)}”?`;
    const sources = bank.flatMap((entry, index) => index !== slot - 1 && entry && identity(entry) === identity(item) ? [index + 1] : []);
    const move = sources.length ? `Move “${name(item)}” from preset ${sources.join(', ')} to preset ${slot}. The speaker may empty the old slot${sources.length > 1 ? 's' : ''}.` : `Save “${name(item)}” to preset ${slot}.`;
    return `${move}${target ? ` Replace “${name(target)}” in preset ${slot}.` : ''}`;
}

export function presetOutcomeMatches(before, after, slot, item, { rename = false } = {}) {
    if (!equivalentContent(item, after[slot - 1], rename)) return false;
    return before.every((entry, index) => index === slot - 1 || equivalentContent(entry, after[index]) ||
        (item && entry && identity(entry) === identity(item) && after[index] === null));
}

// One operation owns its frozen content, fresh bank and target identity. A
// transport failure can still mean the write happened; only reads are retried.
export function createPresetMutations({ api, deviceId, physicalId, epoch, isCurrent,
    onReadback, onChange, delays = [0, 150, 500], pause = ms => new Promise(resolve => setTimeout(resolve, ms)) }) {
    let ticket = 0;
    let operation = null;
    let locked = false;
    let disposed = false;
    const live = token => !disposed && ticket === token && isCurrent();
    const show = state => { if (!disposed && isCurrent()) onChange(state); };
    async function read(token, expected) {
        const headers = physicalId ? { 'X-AfterTouch-Preset-Target': physicalId } : {};
        const response = await timedRequest(signal => api.devicePresets(deviceId, { headers, signal }));
        if (!live(token)) throw new Error('Target changed.');
        const data = response?.data;
        if (response?.success !== true || !data?.info?.device_id || !data?.status?.presets ||
            (data.status.presets.Preset != null && !Array.isArray(data.status.presets.Preset)) || data.status.epoch === undefined) throw new Error('Fresh presets unavailable.');
        const target = JSON.stringify([data.info.device_id, data.status.epoch]);
        if ((physicalId && data.info.device_id !== physicalId) || (epoch != null && data.status.epoch !== epoch) ||
            (expected && target !== expected)) throw new Error('Speaker identity changed. Reopen the editor.');
        const bank = presetBank(data.status);
        onReadback?.(deviceId, data.status, data.info);
        return { bank, target };
    }
    const mutations = {
        isPending: () => locked || operation !== null,
        async request(slot, content, { rename = false, moveFrom = null, authorizeEmpty = false } = {}) {
            if (locked || disposed || !isCurrent() || !Number.isInteger(Number(slot)) || slot < 1 || slot > 6) return;
            locked = true;
            const token = ++ticket;
            const item = presetContent(content);
            operation = null;
            show({ phase: 'preflight', message: 'Reading the speaker’s presets…' });
            try {
                if (item && (!item.source || !item.location)) throw new Error('This content cannot be saved as a preset.');
                const snapshot = await read(token);
                if (!live(token)) return;
                if (moveFrom !== null && (!Number.isInteger(moveFrom) || moveFrom < 1 || moveFrom > 6 || moveFrom === Number(slot) ||
                    !item || identity(snapshot.bank[moveFrom - 1]) !== identity(item))) throw new Error('The source preset changed. Reopen the editor.');
                if (moveFrom === null && (same(snapshot.bank[slot - 1], item) || (!rename && item && identity(snapshot.bank[slot - 1]) === identity(item)))) {
                    show({ phase: 'unchanged', message: item ? `“${name(item)}” is already in preset ${slot}. No change made.` : `Preset ${slot} is already empty. No change made.` });
                    return;
                }
                operation = { slot: Number(slot), item, rename, moveFrom, ...snapshot };
                // A completed hold authorizes only a fresh, simple empty-slot
                // save. confirm still performs its second read and asks again
                // on any bank drift, before the single write.
                if (authorizeEmpty === true && item && !rename && moveFrom === null && !snapshot.bank[slot - 1] &&
                    !snapshot.bank.some(entry => entry && identity(entry) === identity(item))) {
                    locked = false;
                    await mutations.confirm();
                } else {
                    show({ phase: 'confirm', message: presetWarning(snapshot.bank, slot, item) + (moveFrom !== null ? ` Preset ${moveFrom} will be cleared after the destination is verified if the speaker keeps a copy there.` : '') });
                }
            } catch (error) {
                operation = null;
                if (live(token)) show({ phase: 'failed', message: `${error.message} No preset was written.` });
            } finally { if (live(token)) locked = false; }
        },
        async confirm() {
            if (locked || !operation || disposed || !isCurrent()) return;
            locked = true;
            const token = ticket;
            const op = operation;
            let destinationAlreadySaved = false;
            show({ phase: 'checking', message: 'Checking presets before saving…' });
            try {
                const latest = await read(token, op.target);
                if (!live(token)) return;
                if (!same(latest.bank, op.bank)) {
                    if (op.moveFrom !== null && identity(latest.bank[op.moveFrom - 1]) !== identity(op.item)) throw new Error('The source preset changed. Reopen the editor.');
                    operation = { ...op, bank: latest.bank };
                    show({ phase: 'confirm', message: `Presets changed. Review again: ${presetWarning(latest.bank, op.slot, op.item)}${op.moveFrom !== null ? ` Preset ${op.moveFrom} will be cleared after the destination is verified if the speaker keeps a copy there.` : ''}` });
                    return;
                }
                destinationAlreadySaved = op.moveFrom !== null && equivalentContent(op.item, latest.bank[op.slot - 1], false);
            } catch (error) {
                operation = null;
                if (live(token)) show({ phase: 'failed', message: `${error.message} No preset was written.` });
                if (live(token)) locked = false;
                return;
            } finally { if (live(token) && operation !== op) locked = false; }
            if (!live(token)) return;
            operation = null;
            show({ phase: 'writing', message: 'Saving preset and checking all six slots…' });
            let writeError = null;
            try {
                const headers = { 'X-AfterTouch-Preset-Target': JSON.parse(op.target)[0] };
                const result = destinationAlreadySaved ? { success: true } : await timedRequest(signal => op.item ? api.storePresetContent(deviceId, op.slot, op.item, { headers, signal }) : api.removePreset(deviceId, op.slot, { headers, signal }));
                if (result?.success !== true) writeError = result?.error || 'The speaker did not acknowledge the request.';
            } catch (_) { writeError = 'The save response was lost.'; }
            for (const delay of delays) {
                if (!live(token)) return;
                if (delay) await pause(delay);
                if (!live(token)) return;
                try {
                    const result = await read(token, op.target);
                    if (presetOutcomeMatches(op.bank, result.bank, op.slot, op.item, { rename: op.rename })) {
                        if (op.moveFrom !== null && result.bank[op.moveFrom - 1]) {
                            show({ phase: 'writing', message: `Preset ${op.slot} verified. Clearing the original preset ${op.moveFrom}…` });
                            try {
                                const headers = { 'X-AfterTouch-Preset-Target': JSON.parse(op.target)[0] };
                                await timedRequest(signal => api.removePreset(deviceId, op.moveFrom, { headers, signal }));
                            } catch (_) { /* A lost response is ambiguous; verify, never repeat. */ }
                            let removed = false;
                            for (const clearDelay of delays) {
                                if (!live(token)) return;
                                if (clearDelay) await pause(clearDelay);
                                if (!live(token)) return;
                                try {
                                    const cleared = await read(token, op.target);
                                    if (cleared.bank[op.moveFrom - 1] === null && presetOutcomeMatches(result.bank, cleared.bank, op.slot, op.item, { rename: op.rename })) {
                                        removed = true;
                                        break;
                                    }
                                } catch (_) { /* Bounded full-bank readback after the planned clear. */ }
                            }
                            if (!live(token)) return;
                            if (!removed) {
                                show({ phase: 'unverified', message: `Preset ${op.slot} was saved, but removal from preset ${op.moveFrom} is unverified. Check both slots before trying again.` });
                                locked = false;
                                return;
                            }
                        }
                        show({ phase: 'confirmed', message: op.moveFrom !== null
                            ? `Moved to preset ${op.slot}; preset ${op.moveFrom} cleared. All six slots checked.`
                            : `Preset ${op.slot} ${op.item ? 'saved' : 'cleared'}. All six slots checked.` });
                        locked = false;
                        return;
                    }
                } catch (_) { /* Keep the bounded readback attempts; never repeat the write. */ }
            }
            if (live(token)) {
                show({ phase: 'unverified', message: `${writeError ? `${writeError} ` : ''}Change unverified. Check the presets before trying again; the speaker may have changed them.` });
                locked = false;
            }
        },
        cancel() { if (!locked) { ++ticket; operation = null; show(null); } },
        dispose() { disposed = true; ++ticket; operation = null; },
    };
    return mutations;
}
