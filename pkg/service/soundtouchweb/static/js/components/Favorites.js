import { h } from 'preact';
import { useState } from 'preact/hooks';
import htm from 'htm';
import { SourceIcon } from '../sourceIcons.js';
import { sourceLabel } from '../sourceLabels.js';
import { missingSourceFor } from '../sourceAvailability.mjs';
import { CatalogPicker } from './CatalogPicker.js';

const html = htm.bind(h);

export function Favorites({ deviceId, status, favorites, commandBusy = false, onPlay }) {
    const [picking, setPicking] = useState(false);
    const [query, setQuery] = useState('');
    const { entries = [], available, loading, error, busy } = favorites ?? {};
    const needle = query.trim().toLowerCase();
    const filtered = entries.filter(entry => !needle ||
        `${entry.content_item?.ItemName || ''} ${sourceLabel(entry.content_item?.Source)}`.toLowerCase().includes(needle));

    function pick(item) {
        // CatalogPicker's selector supplies a ContentItem; saving never plays it.
        return favorites.add(item).then(() => setPicking(false)).catch(() => {});
    }

    return html`
        <div class="favorites-section" aria-busy=${loading ? 'true' : null}>
            <div class="favorites-toolbar">
                <p class="favorites-note">Saved for all your speakers.</p>
                ${available === true && html`<button type="button" class="favorites-add"
                    disabled=${busy} onClick=${() => setPicking(!picking)}
                    aria-expanded=${picking ? 'true' : 'false'}>Add from catalog</button>`}
            </div>
            ${error && html`<div class="favorites-error" role="alert">
                <span>${error}</span>
                <button type="button" class="favorites-retry" disabled=${loading || busy}
                    onClick=${() => favorites.refresh()}>Retry</button>
            </div>`}
            ${loading && entries.length === 0 && html`<p class="favorites-note" role="status">Loading favorites…</p>`}
            ${!loading && available === false && html`<p class="favorites-note">
                Favorites are unavailable on this service. Enable a persistent favorites store to save them.
            </p>`}
            ${!loading && !error && available === true && entries.length === 0 && html`<p class="favorites-empty">
                No favorites yet. Save what’s playing, add an item from Recents, or choose from the catalog.
            </p>`}
            ${(entries.length > 6 || query) && html`<input type="search" class="favorites-filter"
                aria-label="Filter favorites" placeholder="Filter favorites" value=${query}
                onInput=${event => setQuery(event.target.value)} />`}
            <ul class="favorites-list">
                ${filtered.map(entry => {
                    const item = entry.content_item;
                    if (!item) return null;
                    const name = item.ItemName || item.Location || sourceLabel(item.Source);
                    const missing = missingSourceFor(status?.sources, item.Source, item.SourceAccount);
                    return html`<li class="favorite-row" key=${entry.id} data-source=${item.Source}>
                        <button type="button" class="favorite-play" disabled=${commandBusy || !onPlay}
                            onClick=${() => onPlay({ ID: entry.id, ContentItem: item })}
                            aria-label=${`Play ${name}`} title=${name}>
                            <span class="favorite-thumb">
                                ${item.ContainerArt ? html`<img src=${item.ContainerArt} alt="" />`
                                    : html`<${SourceIcon} source=${item.Source} className="favorite-source-icon" />`}
                            </span>
                            <span class="favorite-info">
                                <span class="favorite-name">${name}</span>
                                <span class="favorite-source">${sourceLabel(item.Source)}</span>
                                ${missing && html`<span class="favorite-warning">${missing}</span>`}
                            </span>
                            <span aria-hidden="true">▶</span>
                        </button>
                        <button type="button" class="favorite-remove" disabled=${busy}
                            aria-label=${`Remove ${name} from favorites`} title="Remove from favorites"
                            onClick=${() => favorites.remove(entry.id).catch(() => {})}>×</button>
                    </li>`;
                })}
            </ul>
            ${needle && entries.length > 0 && filtered.length === 0 && html`<p class="favorites-empty">Nothing matches that filter.</p>`}
            ${picking && html`<${CatalogPicker} deviceId=${deviceId} sources=${status?.sources}
                presets=${status?.presets} onPick=${pick} onClose=${() => setPicking(false)} />`}
        </div>
    `;
}
