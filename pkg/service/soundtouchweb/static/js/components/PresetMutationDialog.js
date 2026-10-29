import { h } from 'preact';
import { useEffect, useRef } from 'preact/hooks';
import htm from 'htm';

const html = htm.bind(h);
export function PresetMutationDialog({ mutations }) {
    const state = mutations?.state;
    const root = useRef(null);
    const previousFocus = useRef(null);
    useEffect(() => {
        if (state && !previousFocus.current) previousFocus.current = document.activeElement;
        // A repeated Enter key must not accept a newly opened destructive action.
        if (state?.phase === 'confirm') root.current?.querySelector('.preset-cancel')?.focus();
        if (!state && previousFocus.current) {
            if (previousFocus.current.isConnected) previousFocus.current.focus();
            previousFocus.current = null;
        }
    }, [state?.phase, state?.message]);
    if (!state) return null;
    const busy = ['preflight', 'checking', 'writing'].includes(state.phase);
    return html`<section class="preset-safety-dialog" role="dialog" aria-modal="false" ref=${root}
        aria-label="Confirm preset change" aria-busy=${busy ? 'true' : 'false'}>
        <h3>Preset change</h3>
        <p role="status" class=${`preset-outcome ${state.phase}`}>${state.message}</p>
        <div class="preset-safety-actions">
            ${state.phase === 'confirm' && html`<button type="button" class="btn-primary preset-confirm"
                onClick=${mutations.confirm}>Confirm change</button>`}
            <button type="button" class="btn-secondary preset-cancel" disabled=${busy}
                onClick=${mutations.cancel}>${state.phase === 'confirm' ? 'Cancel' : 'Close'}</button>
        </div>
    </section>`;
}
