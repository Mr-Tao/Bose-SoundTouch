import { h } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { identityOf } from '../recentsArt.mjs';

const html = htm.bind(h);

export function saveableContent(item) {
    return !!item?.Source && !!item?.Location &&
        !['STANDBY', 'AUX', 'AIRPLAY', 'RAOP', 'BLUETOOTH'].includes(item.Source.toUpperCase());
}

// Saving to the library is separate from playing or assigning a hardware slot.
export function FavoriteButton({ item, favorites }) {
    const [error, setError] = useState(null);
    const identity = identityOf(item);
    const owner = useRef(identity);
    owner.current = identity;
    const saved = !!favorites?.has(item);
    useEffect(() => setError(null), [identity, saved]);
    if (!saveableContent(item) || favorites?.available !== true) return null;
    return html`<span class="favorite-save-control">
        <button type="button" class="btn-secondary favorite-save" disabled=${saved || favorites.busy}
            aria-label=${saved ? 'Saved to favorites' : `Save ${item.ItemName || item.Source} to favorites`}
            onClick=${event => {
                event.stopPropagation();
                setError(null);
                favorites.add(item).catch(err => {
                    if (owner.current === identity) setError(err?.message || 'Could not save favorite.');
                });
            }}>${saved ? 'Saved to favorites' : 'Save favorite'}</button>
        ${error && html`<span class="favorite-save-error" role="alert">${error}</span>`}
    </span>`;
}
