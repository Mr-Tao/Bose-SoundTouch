import { h } from 'preact';
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { api } from '../api.js';
import { SourceIcon } from '../sourceIcons.js';
import { sourceLabel } from '../sourceLabels.js';
import { CatalogPicker } from './CatalogPicker.js';
import { StoredPresets } from './StoredPresets.js';
import { canonicalLocation, sameContentLocation } from '../contentLocation.mjs';
import { currentPresetContent, saveGridPreset } from '../presetMove.mjs';
import { createPresetHoldGesture } from '../presetHold.mjs';
import { usePresetTarget } from '../usePresetTarget.js';

const html = htm.bind(h);
const slotNumbers = [1, 2, 3, 4, 5, 6];

function PresetSlot({ preset, active, command, busy, gesture, hold }) {
    const item = preset.ContentItem;
    const name = item?.ItemName || `Preset ${preset.ID}`;
    return html`
        <div class="preset-slot-wrap ${active ? 'active' : ''}">
            <button type="button"
                class="preset-slot ${item ? '' : 'empty'} ${active ? 'active' : ''} ${hold ? 'preset-holding' : ''} ${hold?.phase === 'ready' ? 'preset-hold-ready' : ''}"
                data-source=${item?.Source ?? ''}
                data-preset-slot=${preset.ID}
                onPointerDown=${event => gesture.down(event, Number(preset.ID))}
                onPointerMove=${gesture.move} onPointerUp=${gesture.up}
                onPointerCancel=${gesture.interrupted} onLostPointerCapture=${gesture.interrupted}
                onContextMenu=${event => event.preventDefault()} onDragStart=${event => event.preventDefault()}
                onClick=${event => gesture.click(event, Number(preset.ID))}
                onKeyDown=${event => { if (event.key === 'Escape') gesture.interrupted(); }}
                disabled=${busy} aria-pressed=${active ? 'true' : 'false'}
                aria-label=${`Preset ${preset.ID}: ${item ? name : 'empty, edit preset'}`}
                aria-busy=${command?.action === 'preset' && busy && command?.expected?.targetId === String(preset.ID) ? 'true' : null}
                title=${item ? name : `Edit empty preset ${preset.ID}`}>
                <span class="preset-hold-fill" aria-hidden="true"></span>
                <span class="preset-thumb">
                    ${item?.ContainerArt
                        ? html`<img class="preset-art" src=${item.ContainerArt} alt="" />`
                        : item && html`<${SourceIcon} source=${item.Source} className="preset-source-icon" />`}
                </span>
                <span class="preset-text">
                    ${item && html`<span class="preset-source-label">${sourceLabel(item.Source)}</span>`}
                    <span class="preset-name">${item ? name : 'Empty · Edit'}</span>
                    ${hold && html`<span class="preset-hold-caption" aria-hidden="true">
                        <span class="preset-hold-label">${hold.phase === 'ready' ? 'Release to save' : 'Save current playback'}</span>
                        <span class="preset-hold-name">${hold.item.ItemName || 'Current playback'}</span>
                    </span>`}
                </span>
                <span class="preset-num">${preset.ID}</span>
            </button>
        </div>`;
}

function confirmationMessage(slot, item, { bank, duplicates }) {
    const name = item.ItemName || 'current playback';
    const previous = bank[slot]?.itemName || `preset ${slot}`;
    const action = bank[slot] ? `Replace “${previous}” in preset ${slot} with “${name}”?` : `Save “${name}” to preset ${slot}?`;
    const sources = duplicates.map(id => `preset ${id} (“${bank[id].itemName || 'Unnamed station'}”)`).join(', ');
    return sources ? `${action} The speaker may remove it from ${sources}.` : action;
}

export function Presets({ deviceId, physicalId, status, command, commandBusy = false, onSelect }) {
    const [editingSlot, setEditingSlot] = useState(null);
    const [hold, setHold] = useState(null);
    const [notice, setNotice] = useState('');
    const [saveState, setSaveState] = useState(null);
    const latest = useRef(null);
    const pending = useRef(false);
    const confirmationText = useRef(null);
    const target = usePresetTarget(deviceId, physicalId, status?.epoch);
    const owner = JSON.stringify([deviceId, physicalId, status?.epoch]);
    const presets = status?.presets?.Preset ?? [];
    const byId = Object.fromEntries(presets.map(preset => [preset.ID, preset]));
    const current = currentPresetContent(status?.nowPlaying);
    const account = current?.SourceAccount === current?.Source ? '' : current?.SourceAccount || '';
    const identity = current ? JSON.stringify([current.Source, account, canonicalLocation(current.Source, current.Location)]) : '';
    const busy = commandBusy || ['checking', 'confirm'].includes(saveState?.phase);
    const playing = status?.nowPlaying?.ContentItem;
    const isActive = item => item && item.Source === playing?.Source && sameContentLocation(item.Source, item.Location, playing?.Location);
    const activeSlot = slotNumbers.find(id => isActive(byId[id]?.ContentItem));

    async function save(operation, approvedBank) {
        if (pending.current || !operation.target.isCurrent()) return;
        pending.current = true;
        setSaveState({ phase: 'checking' });
        try {
            const confirmation = await saveGridPreset({ api, ...operation.target, slot: operation.slot,
                content: operation.item, hold: operation.hold, approvedBank });
            if (!operation.target.isCurrent()) return;
            setSaveState(confirmation ? { phase: 'confirm', operation, confirmation }
                : { phase: 'confirmed', message: `Preset ${operation.slot} saved.` });
        } catch (error) {
            if (operation.target.isCurrent()) setSaveState({ phase: 'error', message: error.message });
        } finally {
            if (operation.target.isCurrent()) pending.current = false;
        }
    }

    latest.current = { owner, current, identity, byId, deviceId, onSelect, target, save,
        canTap: !busy, canHold: !!current && !busy && editingSlot === null };
    const gesture = useMemo(() => createPresetHoldGesture({
        getSnapshot: () => ({ ...latest.current, canTap: latest.current.canTap && !pending.current,
            canHold: latest.current.canHold && !pending.current }),
        onChange: (next, message = '') => { setHold(next); setNotice(message); },
        onTap: slot => {
            const props = latest.current;
            if (pending.current || !props.canTap) return;
            const preset = props.byId[slot];
            if (!preset?.ContentItem) {
                setSaveState(null);
                setEditingSlot(slot);
            } else if (props.onSelect) {
                props.onSelect(preset);
            } else {
                api.control(props.deviceId, 'preset', slot);
            }
        },
        onSave: (slot, item) => {
            const props = latest.current;
            if (props.canHold) props.save({ slot, item, target: props.target, hold: true });
        },
    }), []);
    useLayoutEffect(() => {
        pending.current = false;
        setSaveState(null);
        setEditingSlot(null);
    }, [owner]);
    useLayoutEffect(() => gesture.attach(), [gesture]);
    useLayoutEffect(() => { gesture.check(); }, [gesture, owner, identity, busy, editingSlot]);
    useLayoutEffect(() => {
        if (saveState?.phase === 'confirm') confirmationText.current?.focus();
    }, [saveState]);

    function edit() {
        gesture.cancel();
        setSaveState(null);
        setEditingSlot(editingSlot === null ? activeSlot || 1 : null);
    }

    return html`
        <div class="presets-section">
            <h3 class="section-title">Presets</h3>
            <${StoredPresets} key=${owner}
                deviceId=${deviceId} physicalId=${physicalId} epoch=${status?.epoch} revision=${status?.revision} />
            <div class="preset-grid">
                ${slotNumbers.map(id => html`<${PresetSlot} key=${owner + ':' + id}
                    preset=${byId[id] ?? { ID: id, ContentItem: null }}
                    active=${isActive(byId[id]?.ContentItem)} command=${command} busy=${busy}
                    gesture=${gesture} hold=${hold?.slot === id ? hold : null} />`)}
            </div>
            <div class="presets-footer">
                ${current && html`<p class="preset-hold-hint">Tap to play · hold to save current playback</p>`}
                <button type="button" class="preset-edit-control" aria-label="Edit presets" aria-expanded=${editingSlot !== null ? 'true' : 'false'}
                    disabled=${busy} onClick=${edit}>Edit</button>
            </div>
            <p class="preset-hold-status" role="status">${notice}</p>
            ${saveState?.phase === 'confirm' && html`<div class="preset-outcome"
                onKeyDown=${event => { if (event.key === 'Escape') setSaveState(null); }}>
                <p role="status" tabIndex="-1" ref=${confirmationText}>
                    ${confirmationMessage(saveState.operation.slot, saveState.operation.item, saveState.confirmation)}</p>
                <button type="button" class="btn-secondary preset-confirm" disabled=${commandBusy}
                    onClick=${() => save(saveState.operation, saveState.confirmation.bank)}>Save preset</button>
                <button type="button" class="btn-secondary preset-cancel" onClick=${() => setSaveState(null)}>Cancel</button>
            </div>`}
            ${saveState?.phase === 'checking' && html`<p class="preset-outcome" role="status">Checking presets…</p>`}
            ${saveState?.phase === 'confirmed' && html`<p class="preset-outcome confirmed" role="status">${saveState.message}</p>`}
            ${saveState?.phase === 'error' && html`<p class="settings-error" role="alert">${saveState.message}</p>`}
            ${editingSlot !== null && html`<div class="preset-editor">
                <label class="preset-editor-choice">Preset
                    <select aria-label="Preset to edit" value=${editingSlot} disabled=${busy}
                        onChange=${event => { setSaveState(null); setEditingSlot(Number(event.currentTarget.value)); }}>
                        ${slotNumbers.map(id => html`<option value=${id}>${id} · ${byId[id]?.ContentItem?.ItemName ||
                            (byId[id]?.ContentItem ? 'Unnamed station' : 'Empty')}</option>`)}
                    </select>
                </label>
                <button type="button" class="btn-secondary preset-save-current" disabled=${!current || busy}
                    onClick=${() => save({ slot: editingSlot, item: current, target })}>Save current here</button>
                <div inert=${busy ? '' : null}>
                    <${CatalogPicker} key=${owner + ':' + editingSlot}
                        deviceId=${deviceId} physicalId=${physicalId} epoch=${status?.epoch}
                        slot=${editingSlot} presets=${status?.presets} sources=${status?.sources} current=${byId[editingSlot] ?? null}
                        onClose=${() => setEditingSlot(null)} onAssigned=${() => setEditingSlot(null)} />
                </div>
            </div>`}
        </div>`;
}
