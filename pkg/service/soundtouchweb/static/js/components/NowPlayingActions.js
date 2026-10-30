import { h } from 'preact';
import { useLayoutEffect, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { identityOf } from '../recentsArt.mjs';
import { sameContentLocation } from '../contentLocation.mjs';
import { currentPresetContent } from '../presetMutations.mjs';
import { saveableContent } from './FavoriteButton.js';
import { PresetPicker } from './PresetPicker.js';

const html = htm.bind(h);

// Actions own a frozen ContentItem and the controller that opened them. Live
// playback updates must never substitute another item or speaker at execution.
export function NowPlayingActions({ nowPlaying, deviceId, presets, favorites, presetMutations }) {
    const [selection, setSelection] = useState(null);
    const [error, setError] = useState(null);
    const root = useRef(null);
    const toggle = useRef(null);
    const menu = useRef(null);
    const selected = useRef(null);
    const latest = useRef(null);
    const mounted = useRef(true);
    const generation = useRef(0);
    const ci = nowPlaying?.ContentItem;
    const presetContent = currentPresetContent(nowPlaying);
    const contentKey = JSON.stringify([nowPlaying?.Source, nowPlaying?.TrackID, ci]);
    const canFavorite = saveableContent(ci) && favorites?.available === true;
    const canPreset = !!deviceId && !!presetContent && !!presetMutations?.request;
    const hasActions = canFavorite || canPreset;
    latest.current = { deviceId, contentKey, item: presetContent || ci, favorites, presetMutations, canFavorite, canPreset,
        add: favorites?.add, remove: favorites?.remove, request: presetMutations?.request };

    function visible() {
        return mounted.current && root.current?.isConnected && !document.hidden &&
            document.visibilityState !== 'hidden' &&
            !root.current.closest('[hidden], [inert], [aria-hidden="true"]') &&
            root.current.getClientRects().length > 0;
    }
    function sameOwner(frozen) {
        const current = latest.current;
        return frozen && current.deviceId === frozen.deviceId && current.contentKey === frozen.contentKey &&
            current.add === frozen.add && current.remove === frozen.remove && current.request === frozen.request && visible();
    }
    function close(restoreFocus = false) {
        generation.current++;
        selected.current = null;
        setSelection(null);
        setError(null);
        if (restoreFocus && visible()) toggle.current?.focus({ preventScroll: true });
    }
    function open() {
        const current = latest.current;
        if ((!current.canFavorite && !current.canPreset) || !visible()) return;
        const frozen = { deviceId: current.deviceId, contentKey: current.contentKey,
            add: current.add, remove: current.remove, request: current.request,
            item: Object.freeze({ ...current.item }) };
        generation.current++;
        selected.current = frozen;
        setError(null);
        setSelection(frozen);
    }

    useLayoutEffect(() => {
        mounted.current = true;
        if (!root.current) return;
        const outside = event => { if (selected.current && !root.current?.contains(event.target)) close(); };
        const cancel = () => { if (selected.current) close(); };
        const visibility = () => { if (!visible()) cancel(); };
        const keyDown = event => {
            if (event.key === 'Escape' && selected.current) {
                event.preventDefault();
                close(true);
            }
        };
        const observer = new MutationObserver(visibility);
        for (let node = root.current; node; node = node.parentElement) {
            observer.observe(node, { attributes: true, attributeFilter: ['hidden', 'inert', 'aria-hidden', 'class', 'style'] });
        }
        document.addEventListener('pointerdown', outside, true);
        document.addEventListener('click', outside, true);
        document.addEventListener('keydown', keyDown, true);
        document.addEventListener('scroll', cancel, true);
        document.addEventListener('visibilitychange', visibility);
        window.addEventListener('blur', cancel);
        return () => {
            mounted.current = false;
            generation.current++;
            selected.current = null;
            observer.disconnect();
            document.removeEventListener('pointerdown', outside, true);
            document.removeEventListener('click', outside, true);
            document.removeEventListener('keydown', keyDown, true);
            document.removeEventListener('scroll', cancel, true);
            document.removeEventListener('visibilitychange', visibility);
            window.removeEventListener('blur', cancel);
        };
    }, [hasActions]);
    useLayoutEffect(() => {
        if ((selection || selected.current) &&
            (!hasActions || selection !== selected.current || !sameOwner(selection))) close();
    });
    useLayoutEffect(() => {
        if (selection) menu.current?.querySelector('button:not(:disabled)')?.focus({ preventScroll: true });
    }, [selection]);

    async function favorite() {
        const frozen = selected.current;
        const current = latest.current;
        if (!sameOwner(frozen) || !current.canFavorite || current.favorites.busy) return;
        const entry = current.favorites.entries?.find(entry => identityOf(entry.content_item) === identityOf(frozen.item));
        const version = generation.current;
        setError(null);
        try {
            if (entry) await frozen.remove(entry.id);
            else await frozen.add(frozen.item);
            if (version === generation.current && selected.current === frozen && sameOwner(frozen)) close(true);
        } catch (err) {
            if (version === generation.current && selected.current === frozen && sameOwner(frozen)) {
                setError(err?.message || 'Could not update favorites.');
            }
        }
    }

    if (!hasActions) return null;
    const openMenu = !!selection && sameOwner(selection);
    const saved = openMenu && favorites?.entries?.some(entry => identityOf(entry.content_item) === identityOf(selection.item));
    const mappedPreset = openMenu && presets?.Preset?.find(preset =>
        preset.ContentItem?.Source === selection.item.Source &&
        sameContentLocation(selection.item.Source, preset.ContentItem?.Location, selection.item.Location));
    return html`<div class="now-playing-actions ${openMenu ? 'recent-actions-open' : ''}" ref=${root}
        onFocusOut=${event => { if (!event.currentTarget.contains(event.relatedTarget)) close(); }}>
        <button type="button" class="recent-actions-toggle now-playing-actions-toggle" ref=${toggle}
            aria-label="Current playback actions" aria-expanded=${openMenu ? 'true' : 'false'}
            onClick=${event => { event.stopPropagation(); if (selected.current) close(); else open(); }}
            onKeyDown=${event => { if (event.key === 'ArrowDown') { event.preventDefault(); open(); } }}>
            <span aria-hidden="true">⋯</span>
        </button>
        ${openMenu && html`<div class="recent-actions-menu now-playing-actions-menu" role="group"
            aria-label="Current playback actions" ref=${menu}>
            ${canFavorite && html`<button type="button" class="favorite-save recent-action" disabled=${favorites.busy}
                onClick=${event => { event.stopPropagation(); void favorite(); }}>${saved ? 'Remove from Favorites' : 'Add to Favorites'}</button>`}
            ${error && html`<span class="favorite-save-error" role="alert">${error}</span>`}
            ${canPreset && html`<${PresetPicker} wrapClass="recent-preset-wrap" buttonClass="preset-picker-btn recent-action"
                overlayClass="preset-picker-overlay recent-preset-overlay" label="Save as preset…"
                mappedSlot=${mappedPreset ? mappedPreset.ID : null} disabled=${!!presetMutations.isPending?.()}
                onSave=${slot => {
                    const frozen = selected.current;
                    const current = latest.current;
                    if (!sameOwner(frozen) || !current.canPreset || current.presetMutations.isPending?.()) return;
                    close();
                    frozen.request(slot, frozen.item);
                }} />`}
        </div>`}
    </div>`;
}
