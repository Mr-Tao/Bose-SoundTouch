import { h } from 'preact';
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { api } from '../api.js';
import { SourceIcon } from '../sourceIcons.js';
import { sourceLabel } from '../sourceLabels.js';
import { CatalogPicker } from './CatalogPicker.js';
import { StoredPresets } from './StoredPresets.js';
import { canonicalLocation, sameContentLocation } from '../contentLocation.mjs';
import { currentPresetContent } from '../presetMutations.mjs';
import { createPresetHoldGesture } from '../presetHold.mjs';

const html = htm.bind(h);

function PresetSlot({ preset, active, command, busy, gesture, hold }) {
    const item = preset.ContentItem;
    const isEmpty = !item;
    const name = item?.ItemName || `Preset ${preset.ID}`;
    return html`<div class="preset-slot-wrap ${active ? 'active' : ''}">
        <button type="button" class="preset-slot ${isEmpty ? 'empty' : ''} ${active ? 'active' : ''} ${hold ? `preset-holding ${hold.phase === 'ready' ? 'preset-hold-ready' : ''}` : ''}"
            data-source=${item?.Source ?? ''}
            data-preset-slot=${preset.ID}
            onPointerDown=${event => gesture.down(event, Number(preset.ID))}
            onPointerMove=${gesture.move} onPointerUp=${gesture.up}
            onPointerCancel=${gesture.interrupted} onLostPointerCapture=${gesture.interrupted}
            onContextMenu=${event => event.preventDefault()} onDragStart=${event => event.preventDefault()}
            onClick=${event => gesture.click(event, Number(preset.ID))}
            onKeyDown=${event => { if (event.key === 'Escape') gesture.interrupted(); }}
            disabled=${busy} aria-pressed=${active ? 'true' : 'false'}
            aria-label=${`Preset ${preset.ID}: ${isEmpty ? 'empty, edit preset' : name}`}
            aria-busy=${command?.action === 'preset' && busy && command?.expected?.targetId === String(preset.ID) ? 'true' : null}
            title=${isEmpty ? `Edit empty preset ${preset.ID}` : name}>
            <span class="preset-hold-fill" aria-hidden="true"></span>
            <span class="preset-thumb">${item?.ContainerArt
                ? html`<img class="preset-art" src=${item.ContainerArt} alt="" />`
                : item ? html`<${SourceIcon} source=${item.Source} className="preset-source-icon" />` : null}</span>
            <span class="preset-text">
                ${item && html`<span class="preset-source-label">${sourceLabel(item.Source)}</span>`}
                <span class="preset-name">${isEmpty ? 'Empty · Edit' : name}</span>
                ${hold && html`<span class="preset-hold-caption" aria-hidden="true">
                    <span class="preset-hold-label">${hold.phase === 'ready' ? 'Release to save' : 'Save current playback'}</span>
                    <span class="preset-hold-name">${hold.item.ItemName || 'Current playback'}</span>
                </span>`}
            </span>
            <span class="preset-num">${preset.ID}</span>
        </button>
    </div>`;
}

export function Presets({ deviceId, status, command, commandBusy = false, onSelect, showHeading = true, presetMutations }) {
    const [editingSlot, setEditingSlot] = useState(null);
    const [hold, setHold] = useState(null);
    const [notice, setNotice] = useState('');
    const latest = useRef(null);
    const editorOwner = useRef(null);
    const byId = Object.fromEntries((status?.presets?.Preset ?? []).map(p => [p.ID, p]));
    const playing = status?.nowPlaying?.ContentItem;
    const current = currentPresetContent(status?.nowPlaying);
    const canSave = !!current;
    const pending = () => !!presetMutations?.isPending?.() || ['preflight', 'confirm', 'checking', 'writing'].includes(presetMutations?.state?.phase);
    const busy = commandBusy || pending();
    const identity = current ? JSON.stringify([current.Source, current.SourceAccount === current.Source ? '' : current.SourceAccount || '', canonicalLocation(current.Source, current.Location)]) : '';
    const owner = JSON.stringify([deviceId, status?.epoch]);
    latest.current = { owner, request: presetMutations?.request, identity, current,
        canTap: !busy, canHold: canSave && !!presetMutations && !busy && editingSlot === null,
        pending, byId, deviceId, onSelect };
    const gesture = useMemo(() => createPresetHoldGesture({
        getSnapshot: () => ({ ...latest.current, canTap: latest.current.canTap && !latest.current.pending(),
            canHold: latest.current.canHold && !latest.current.pending() }),
        onChange: (next, message = '') => { setHold(next); setNotice(message); },
        onTap: slot => {
            const props = latest.current;
            if (props.pending() || !props.canTap) return;
            const preset = props.byId[slot] ?? { ID: slot, ContentItem: null };
            if (!preset.ContentItem) setEditingSlot(slot);
            else if (props.onSelect) props.onSelect(preset);
            else api.control(props.deviceId, 'preset', slot);
        },
        onSave: (slot, item, request) => {
            const props = latest.current;
            if (props.request === request && props.canHold && !props.pending()) request(slot, item, { authorizeEmpty: true });
        },
    }), []);
    useLayoutEffect(() => gesture.attach(), [gesture]);
    useLayoutEffect(() => { gesture.check(); }, [gesture, owner, identity, presetMutations?.request, busy, editingSlot]);
    useLayoutEffect(() => {
        if (editorOwner.current && (editorOwner.current.owner !== owner || editorOwner.current.request !== presetMutations?.request)) setEditingSlot(null);
        editorOwner.current = { owner, request: presetMutations?.request };
    }, [owner, presetMutations?.request]);
    const activeSlot = [1, 2, 3, 4, 5, 6].find(id => {
        const item = byId[id]?.ContentItem;
        return item && item.Source === playing?.Source && sameContentLocation(item.Source, item.Location, playing?.Location);
    });
    return html`<div class="presets-section">
        ${showHeading && html`<h3 class="section-title">Presets</h3>`}
        <${StoredPresets} deviceId=${deviceId} revision=${status?.revision} presetMutations=${presetMutations} />
        <div class="preset-grid">${[1, 2, 3, 4, 5, 6].map(id => {
            const preset = byId[id] ?? { ID: id, ContentItem: null };
            const item = preset.ContentItem;
            const active = item && item.Source === playing?.Source && sameContentLocation(item.Source, item.Location, playing?.Location);
            return html`<${PresetSlot} key=${id} preset=${preset} active=${active}
                command=${command} busy=${busy} gesture=${gesture} hold=${hold?.slot === id ? hold : null} />`;
        })}</div>
        <div class="presets-footer">
            ${canSave && presetMutations && html`<p class="preset-hold-hint">Tap to play · hold to save current playback</p>`}
            <button type="button" class="preset-edit-control" aria-label="Edit presets" aria-expanded=${editingSlot !== null ? 'true' : 'false'}
                onClick=${() => { gesture.cancel(); setEditingSlot(editingSlot === null ? activeSlot || 1 : null); }}>Edit</button>
        </div>
        <p class="preset-hold-status" role="status">${notice}</p>
        ${editingSlot !== null && html`<div class="preset-editor">
            <label class="preset-editor-choice">Preset
                <select aria-label="Preset to edit" value=${editingSlot} onChange=${event => setEditingSlot(Number(event.currentTarget.value))}>
                    ${[1, 2, 3, 4, 5, 6].map(id => html`<option value=${id}>${id} · ${byId[id]?.ContentItem?.ItemName || (byId[id]?.ContentItem ? 'Unnamed station' : 'Empty')}</option>`)}
                </select>
            </label>
            <button type="button" class="btn-secondary preset-save-current" disabled=${!canSave || !presetMutations || busy}
                onClick=${() => { if (current && !pending()) presetMutations.request(editingSlot, current); }}>Save current here</button>
            <${CatalogPicker} key=${`catalog:${owner}:${editingSlot}`} deviceId=${deviceId} slot=${editingSlot}
                presets=${status?.presets} sources=${status?.sources} current=${byId[editingSlot] ?? null}
                presetMutations=${presetMutations} onClose=${() => setEditingSlot(null)} />
        </div>`}
    </div>`;
}
