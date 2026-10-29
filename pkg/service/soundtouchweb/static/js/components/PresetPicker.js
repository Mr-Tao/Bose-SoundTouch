import { h } from 'preact';
import { useState, useEffect, useRef } from 'preact/hooks';
import htm from 'htm';

const html = htm.bind(h);

// Selection only opens the common confirmation flow. HTTP acknowledgements
// cannot produce success feedback here; the controller checks the whole bank.
export function PresetPicker({ onSave, mappedSlot = null, label = 'Save as preset', title, disabled = false,
    wrapClass = 'preset-picker-wrap', buttonClass = 'preset-picker-btn', overlayClass = 'preset-picker-overlay' }) {
    const [open, setOpen] = useState(false);
    const wrapRef = useRef(null);
    useEffect(() => {
        if (!open) return;
        const close = event => { if (!wrapRef.current?.contains(event.target)) setOpen(false); };
        document.addEventListener('click', close, true);
        return () => document.removeEventListener('click', close, true);
    }, [open]);
    return html`<div class=${wrapClass} ref=${wrapRef}>
        <button type="button" class="${buttonClass} preset-save-text ${open ? 'open' : ''}"
            disabled=${disabled}
            onClick=${event => { event.stopPropagation(); setOpen(value => !value); }}
            title=${title || label} aria-label=${label} aria-expanded=${open ? 'true' : 'false'}>${label}</button>
        ${open && html`<div class=${overlayClass}>
            <div class="preset-picker-label">${mappedSlot ? `Already in preset ${mappedSlot}. Choose a slot to review.` : 'Choose a slot to review'}</div>
            <div class="preset-picker-slots">${[1, 2, 3, 4, 5, 6].map(slot => html`
                <button type="button" key=${slot} class="preset-picker-slot"
                    onClick=${event => { event.stopPropagation(); setOpen(false); onSave?.(slot); }}>${slot}</button>`)}
            </div>
        </div>`}
    </div>`;
}
