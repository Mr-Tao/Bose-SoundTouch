import { h } from 'preact';
import { useState } from 'preact/hooks';
import htm from 'htm';
import { api } from '../api.js';
import { SourceIcon } from '../sourceIcons.js';
import { sourceLabel } from '../sourceLabels.js';
import { CatalogPicker } from './CatalogPicker.js';
import { StoredPresets } from './StoredPresets.js';
import { sameContentLocation } from '../contentLocation.mjs';
import { currentPresetContent } from '../presetMutations.mjs';

const html = htm.bind(h);

function PresetSlot({ preset, deviceId, active, command, commandBusy, onSelect, onEdit, editing }) {
    const item = preset.ContentItem;
    const isEmpty = !item;
    const name = item?.ItemName || `Preset ${preset.ID}`;
    return html`<div class="preset-slot-wrap ${active ? 'active' : ''}">
        <button class="preset-slot ${isEmpty ? 'empty' : ''} ${active ? 'active' : ''}"
            data-source=${item?.Source ?? ''}
            onClick=${() => isEmpty ? onEdit(preset.ID) : (onSelect || (() => api.control(deviceId, 'preset', preset.ID)))(preset)}
            disabled=${!isEmpty && commandBusy}
            aria-busy=${command?.action === 'preset' && commandBusy && command?.expected?.targetId === String(preset.ID) ? 'true' : null}
            title=${isEmpty ? `Edit empty preset ${preset.ID}` : name}>
            <span class="preset-thumb">${item?.ContainerArt
                ? html`<img class="preset-art" src=${item.ContainerArt} alt="" />`
                : item ? html`<${SourceIcon} source=${item.Source} className="preset-source-icon" />` : null}</span>
            <span class="preset-text">
                ${item && html`<span class="preset-source-label">${sourceLabel(item.Source)}</span>`}
                <span class="preset-name">${isEmpty ? 'Empty · Edit' : name}</span>
            </span>
            <span class="preset-num">${preset.ID}</span>
        </button>
        <button type="button" class="preset-edit-control" aria-label=${`Edit preset ${preset.ID}`}
            aria-expanded=${editing ? 'true' : 'false'}
            onClick=${() => onEdit(editing ? null : preset.ID)}>Edit</button>
    </div>`;
}

export function Presets({ deviceId, status, command, commandBusy = false, onSelect, showHeading = true, presetMutations }) {
    const [editingSlot, setEditingSlot] = useState(null);
    const byId = Object.fromEntries((status?.presets?.Preset ?? []).map(p => [p.ID, p]));
    const playing = status?.nowPlaying?.ContentItem;
    const current = currentPresetContent(status?.nowPlaying);
    const canSave = !!current;
    return html`<div class="presets-section">
        ${showHeading && html`<h3 class="section-title">Presets</h3>`}
        <${StoredPresets} deviceId=${deviceId} revision=${status?.revision} presetMutations=${presetMutations} />
        <div class="preset-grid">${[1, 2, 3, 4, 5, 6].map(id => {
            const preset = byId[id] ?? { ID: id, ContentItem: null };
            const item = preset.ContentItem;
            const active = item && item.Source === playing?.Source && sameContentLocation(item.Source, item.Location, playing?.Location);
            return html`<${PresetSlot} key=${id} preset=${preset} deviceId=${deviceId} active=${active}
                command=${command} commandBusy=${commandBusy} onSelect=${onSelect}
                onEdit=${setEditingSlot} editing=${editingSlot === id} />`;
        })}</div>
        ${editingSlot !== null && html`<div class="preset-editor">
            <button type="button" class="btn-secondary preset-save-current" disabled=${!canSave || !presetMutations}
                onClick=${() => { if (current) presetMutations.request(editingSlot, current); }}>Save current here</button>
            <${CatalogPicker} key=${`catalog:${editingSlot}`} deviceId=${deviceId} slot=${editingSlot}
                presets=${status?.presets} sources=${status?.sources} current=${byId[editingSlot] ?? null}
                presetMutations=${presetMutations} onClose=${() => setEditingSlot(null)} />
        </div>`}
    </div>`;
}
