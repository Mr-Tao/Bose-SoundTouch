import { h } from 'preact';
import { useLayoutEffect, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { SourceIcon } from '../sourceIcons.js';
import { identityOf } from '../recentsArt.mjs';
import { saveableContent } from './FavoriteButton.js';
import { PresetPicker } from './PresetPicker.js';

const html = htm.bind(h);
const HOLD_MS = 500;
const MOVE_PX = 12;

// This component is keyed by both the history ID and its complete content.
// Playback keeps the existing row handler; saving uses a separate frozen copy.
export function RecentRow({ rowKey, item, art, command, commandBusy, onPlay,
    favorites, presetMutations, menuOwner, setMenuOwner }) {
    const [selection, setSelection] = useState(null);
    const [error, setError] = useState(null);
    const row = useRef(null);
    const toggle = useRef(null);
    const menu = useRef(null);
    const selected = useRef(null);
    const gesture = useRef(null);
    const pointers = useRef(new Set());
    const suppressClick = useRef(false);
    const generation = useRef(0);
    const mounted = useRef(true);
    const latest = useRef(null);
    const ci = item.ContentItem;
    const canFavorite = saveableContent(ci) && favorites?.available === true;
    const canPreset = saveableContent(ci) && ci.IsPresetable !== false && !!presetMutations?.request;
    const hasActions = canFavorite || canPreset;
    latest.current = { favorites, presetMutations, menuOwner, commandBusy, hasActions,
        add: favorites?.add, remove: favorites?.remove, request: presetMutations?.request };

    function visible() {
        return mounted.current && row.current?.isConnected && !document.hidden &&
            document.visibilityState !== 'hidden' &&
            !row.current.closest('[hidden], [inert], [aria-hidden="true"]') &&
            row.current.getClientRects().length > 0;
    }
    function sameOwner(snapshot) {
        const current = latest.current;
        return snapshot && current.add === snapshot.add && current.remove === snapshot.remove &&
            current.request === snapshot.request && visible();
    }
    function stopGesture(consume = true) {
        const previous = gesture.current;
        if (!previous) return;
        gesture.current = null;
        clearTimeout(previous.timer);
        if (consume) suppressClick.current = true;
        try {
            if (previous.button.hasPointerCapture(previous.id)) previous.button.releasePointerCapture(previous.id);
        } catch (_) { /* The browser may already have released capture. */ }
    }
    function closeMenu(restoreFocus = false) {
        generation.current++;
        selected.current = null;
        setSelection(null);
        setError(null);
        setMenuOwner(owner => owner === rowKey ? null : owner);
        if (restoreFocus && visible()) toggle.current?.focus({ preventScroll: true });
    }
    function cancel() {
        stopGesture();
        if (selected.current) closeMenu();
    }
    function snapshot() {
        const current = latest.current;
        return { add: current.add, remove: current.remove, request: current.request,
            item: Object.freeze({ ...ci, ContainerArt: art || ci.ContainerArt || '' }) };
    }
    function openMenu(frozen = snapshot(), focus = true) {
        if (!latest.current.hasActions || !sameOwner(frozen)) return;
        generation.current++;
        const next = { ...frozen, focus,
            up: row.current.getBoundingClientRect().bottom + 250 > window.innerHeight };
        selected.current = next;
        setError(null);
        setSelection(next);
        setMenuOwner(rowKey);
    }

    useLayoutEffect(() => {
        mounted.current = true;
        const pointerDown = event => {
            pointers.current.add(event.pointerId);
            if (pointers.current.size > 1 || event.isPrimary === false) cancel();
            else if (selected.current && !row.current?.contains(event.target)) closeMenu();
        };
        const pointerEnd = event => pointers.current.delete(event.pointerId);
        const move = event => {
            const g = gesture.current;
            if (g?.id === event.pointerId && Math.hypot(event.clientX - g.x, event.clientY - g.y) > MOVE_PX) cancel();
        };
        const up = event => {
            const g = gesture.current;
            if (g?.id !== event.pointerId) return;
            const bounds = g.button.getBoundingClientRect();
            const inside = event.clientX >= bounds.left && event.clientX <= bounds.right &&
                event.clientY >= bounds.top && event.clientY <= bounds.bottom;
            if (!inside || !sameOwner(g.snapshot) || latest.current.commandBusy) cancel();
            else stopGesture(g.held);
        };
        const interrupted = event => {
            if (gesture.current?.id === event.pointerId) cancel();
        };
        const outside = event => {
            if (selected.current && !row.current?.contains(event.target)) closeMenu();
        };
        const keyDown = event => {
            if (event.key !== 'Escape' || (!selected.current && !gesture.current)) return;
            event.preventDefault();
            stopGesture();
            closeMenu(true);
        };
        const leave = () => { pointers.current.clear(); cancel(); };
        const visibility = () => { if (!visible()) leave(); };
        // Observe the actual ancestors, including the library's hidden panel.
        const observer = new MutationObserver(visibility);
        for (let node = row.current; node; node = node.parentElement) {
            observer.observe(node, { attributes: true, attributeFilter: ['hidden', 'inert', 'aria-hidden', 'class', 'style'] });
        }
        document.addEventListener('pointerdown', pointerDown, true);
        document.addEventListener('pointermove', move, true);
        document.addEventListener('pointerup', up, true);
        document.addEventListener('pointerup', pointerEnd, true);
        document.addEventListener('pointercancel', interrupted, true);
        document.addEventListener('pointercancel', pointerEnd, true);
        document.addEventListener('click', outside, true);
        document.addEventListener('keydown', keyDown, true);
        document.addEventListener('scroll', cancel, true);
        document.addEventListener('visibilitychange', visibility);
        window.addEventListener('blur', leave);
        return () => {
            mounted.current = false;
            generation.current++;
            selected.current = null;
            stopGesture();
            observer.disconnect();
            document.removeEventListener('pointerdown', pointerDown, true);
            document.removeEventListener('pointermove', move, true);
            document.removeEventListener('pointerup', up, true);
            document.removeEventListener('pointerup', pointerEnd, true);
            document.removeEventListener('pointercancel', interrupted, true);
            document.removeEventListener('pointercancel', pointerEnd, true);
            document.removeEventListener('click', outside, true);
            document.removeEventListener('keydown', keyDown, true);
            document.removeEventListener('scroll', cancel, true);
            document.removeEventListener('visibilitychange', visibility);
            window.removeEventListener('blur', leave);
        };
    }, []);

    useLayoutEffect(() => {
        if (gesture.current && (!sameOwner(gesture.current.snapshot) || commandBusy || (gesture.current.canHold && !hasActions))) stopGesture();
        if (selected.current && (!sameOwner(selected.current) || menuOwner !== rowKey || !hasActions)) closeMenu();
    });
    useLayoutEffect(() => {
        if (selection?.focus && menuOwner === rowKey) menu.current?.querySelector('button:not(:disabled)')?.focus({ preventScroll: true });
    }, [selection, menuOwner]);

    function down(event) {
        stopGesture();
        suppressClick.current = false;
        if (event.button !== 0 || event.isPrimary === false || pointers.current.size > 1 || commandBusy || !visible()) {
            suppressClick.current = true;
            return;
        }
        const g = { id: event.pointerId, button: event.currentTarget, x: event.clientX, y: event.clientY,
            snapshot: snapshot(), canHold: hasActions, held: false, timer: null };
        gesture.current = g;
        try { g.button.setPointerCapture(g.id); } catch (_) { /* Synthetic pointers do not own browser capture. */ }
        if (!hasActions) return;
        g.timer = setTimeout(() => {
            if (gesture.current !== g) return;
            if (!sameOwner(g.snapshot) || latest.current.commandBusy || !latest.current.hasActions) { cancel(); return; }
            g.held = true;
            suppressClick.current = true;
            // Keep pointer focus in the row until release; ellipsis/keyboard
            // opening moves focus to the first available action immediately.
            openMenu(g.snapshot, false);
        }, HOLD_MS);
    }
    function click(event) {
        const keyboard = event.detail === 0 && !event.pointerType;
        const consumed = suppressClick.current;
        suppressClick.current = false;
        if ((!keyboard && consumed) || commandBusy || !visible()) {
            event.preventDefault();
            return;
        }
        stopGesture(false);
        if (selected.current) closeMenu();
        onPlay(item);
    }
    async function favorite() {
        const frozen = selected.current;
        const current = latest.current;
        if (!sameOwner(frozen) || current.menuOwner !== rowKey || current.favorites?.busy || current.favorites?.available !== true) return;
        const entry = current.favorites.entries?.find(entry => identityOf(entry.content_item) === identityOf(frozen.item));
        const version = generation.current;
        setError(null);
        try {
            if (entry) await frozen.remove(entry.id);
            else await frozen.add(frozen.item);
            if (version === generation.current && selected.current === frozen && sameOwner(frozen)) closeMenu(true);
        } catch (err) {
            if (version === generation.current && selected.current === frozen && sameOwner(frozen)) {
                setError(err?.message || 'Could not update favorites.');
            }
        }
    }

    const open = !!selection && menuOwner === rowKey;
    const saved = open && favorites?.entries?.some(entry => identityOf(entry.content_item) === identityOf(selection.item));
    const name = ci.ItemName || ci.Source;
    return html`<div class="recent-row ${open ? 'recent-actions-open' : ''}" ref=${row}
        onFocusOut=${event => { if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget)) cancel(); }}>
        <button type="button" class="recent-item" onClick=${click} onPointerDown=${down}
            onLostPointerCapture=${event => { if (gesture.current?.id === event.pointerId) cancel(); }}
            onContextMenu=${event => { if (hasActions) event.preventDefault(); }}
            onDragStart=${event => event.preventDefault()}
            onKeyDown=${event => {
                if (hasActions && (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10'))) {
                    event.preventDefault(); stopGesture(); openMenu();
                }
            }}
            disabled=${commandBusy} aria-busy=${command?.action === 'recent' && commandBusy &&
                command?.expected?.targetId === String(item.ID || item.UTCTime || ci.Location) ? 'true' : null}>
            ${art ? html`<img class="recent-art" src=${art} alt="" />`
                : html`<div class="recent-art recent-art-empty"><${SourceIcon} source=${ci.Source} className="recent-source-icon" /></div>`}
            <div class="recent-info"><span class="recent-name">${name}</span><span class="recent-source">${ci.Source}</span></div>
            <span class="recent-play">▶</span>
        </button>
        ${hasActions && html`<button type="button" class="recent-actions-toggle" ref=${toggle}
            aria-label=${`Actions for ${name}`} aria-expanded=${open ? 'true' : 'false'}
            onPointerDown=${() => stopGesture()}
            onClick=${event => { event.stopPropagation(); if (open) closeMenu(); else openMenu(); }}
            onKeyDown=${event => {
                if (event.key === 'ArrowDown') { event.preventDefault(); openMenu(); }
            }}><span aria-hidden="true">⋯</span></button>`}
        ${open && html`<div class="recent-actions-menu ${selection.up ? 'recent-actions-up' : ''}"
            role="group" aria-label=${`Actions for ${selection.item.ItemName || selection.item.Source}`} ref=${menu}>
            ${canFavorite && html`<button type="button" class="favorite-save recent-action" disabled=${favorites.busy}
                onClick=${event => { event.stopPropagation(); void favorite(); }}>${saved ? 'Remove from Favorites' : 'Add to Favorites'}</button>`}
            ${error && html`<span class="favorite-save-error" role="alert">${error}</span>`}
            ${canPreset && html`<${PresetPicker} wrapClass="recent-preset-wrap" buttonClass="preset-picker-btn recent-action"
                overlayClass="preset-picker-overlay recent-preset-overlay" label="Save as preset…"
                disabled=${!!presetMutations.isPending?.()}
                onSave=${slot => {
                    const frozen = selected.current;
                    if (!sameOwner(frozen) || latest.current.menuOwner !== rowKey || latest.current.presetMutations?.isPending?.()) return;
                    closeMenu();
                    frozen.request(slot, frozen.item);
                }} />`}
        </div>`}
    </div>`;
}
