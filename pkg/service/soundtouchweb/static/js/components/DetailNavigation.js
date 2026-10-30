import { h } from 'preact';
import { useEffect, useRef } from 'preact/hooks';
import htm from 'htm';

const html = htm.bind(h);

// The detail owns its resource heading; navigation here is global. Keep every
// existing destination while giving the playback page a single-row phone bar.
export function DetailNavigation({ onNavigate, onDiscover, isDiscovering }) {
    const root = useRef(null);
    function close(restoreFocus = false) {
        root.current?.querySelectorAll('details[open]').forEach(details => {
            details.open = false;
            if (restoreFocus) details.querySelector('summary')?.focus({ preventScroll: true });
        });
    }
    useEffect(() => {
        const outside = event => { if (!root.current?.contains(event.target)) close(); };
        const escape = event => {
            if (event.key === 'Escape' && root.current?.querySelector('details[open]')) {
                event.preventDefault(); close(true);
            }
        };
        document.addEventListener('pointerdown', outside, true);
        document.addEventListener('keydown', escape, true);
        return () => {
            document.removeEventListener('pointerdown', outside, true);
            document.removeEventListener('keydown', escape, true);
        };
    }, []);
    function navigate(event, page) {
        event.preventDefault(); close(); onNavigate(page);
    }
    function opened(event) {
        if (!event.currentTarget.open) return;
        root.current?.querySelectorAll('details[open]').forEach(details => {
            if (details !== event.currentTarget) details.open = false;
        });
    }
    return html`<div class="detail-navigation" ref=${root}
        onFocusOut=${event => { if (!event.currentTarget.contains(event.relatedTarget)) close(); }}>
        <a href="#" class="detail-nav-devices" title="Devices" aria-label="Devices"
            onClick=${event => navigate(event, 'devices')}>
            <img src="/app/static/img/speaker-mono.svg" alt="" />
        </a>
        <details class="detail-nav-menu" onToggle=${opened}>
            <summary>Sources <span aria-hidden="true">⌄</span></summary>
            <div class="detail-nav-options">
                ${[['tunein', 'TuneIn'], ['radiobrowser', 'RadioBrowser'], ['playurl', 'Play URL'], ['library', 'Music library']].map(([page, name]) => html`
                    <a href="#" key=${page} onClick=${event => navigate(event, page)}>${name}</a>`)}
            </div>
        </details>
        <details class="detail-nav-menu" onToggle=${opened}>
            <summary aria-label="More navigation"><span aria-hidden="true">⋯</span></summary>
            <div class="detail-nav-options">
                <a href="#" onClick=${event => navigate(event, 'tts')}>Text to speech</a>
                <button type="button" disabled=${isDiscovering} onClick=${() => { close(); onDiscover(); }}>
                    ${isDiscovering ? 'Discovering…' : 'Discover speakers'}</button>
                <a href="https://gesellix.github.io/Bose-SoundTouch/" target="_blank" rel="noopener"
                    onClick=${() => close()}>Documentation</a>
            </div>
        </details>
    </div>`;
}
