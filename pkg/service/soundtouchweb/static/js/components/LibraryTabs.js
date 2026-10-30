import { h } from 'preact';
import { useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { Presets } from './Presets.js';
import { Recents } from './Recents.js';
import { Favorites } from './Favorites.js';
import { libraryOwnerCaption } from '../detailPresentation.mjs';

const html = htm.bind(h);
const tabs = ['presets', 'favorites', 'recents'];

export function LibraryTabs({ deviceId, device, status, command, commandBusy = false,
    onSelectPreset, onPlay, onPlayFavorite = onPlay, selectedTab, onTabChange, recentsRefreshToken = 0, favorites, presetMutations }) {
    const tabRefs = useRef([]);
    const [focused, setFocused] = useState(null);
    const [localTab, setLocalTab] = useState('presets');
    const selected = tabs.includes(selectedTab) ? selectedTab : localTab;
    function select(tab) { setLocalTab(tab); onTabChange?.(tab); }
    const prefix = `library-${encodeURIComponent(deviceId || 'speaker')}`;

    function keyDown(event, index) {
        let next;
        if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
        else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = tabs.length - 1;
        else if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            select(tabs[index]);
            return;
        } else return;
        event.preventDefault();
        setFocused(tabs[next]);
        tabRefs.current[next]?.focus();
    }

    return html`
        <section class="library-section" aria-label="Speaker library">
            <div class="library-tabs" role="tablist" aria-label="Speaker library"
                onFocusOut=${event => {
                    if (!event.currentTarget.contains(event.relatedTarget)) setFocused(null);
                }}>
                ${tabs.map((tab, index) => html`<button type="button" key=${tab}
                    ref=${node => { tabRefs.current[index] = node; }}
                    id=${`${prefix}-tab-${tab}`} role="tab"
                    aria-controls=${`${prefix}-panel-${tab}`} aria-selected=${selected === tab ? 'true' : 'false'}
                    tabIndex=${(focused || selected) === tab ? 0 : -1}
                    onFocus=${() => setFocused(tab)} onKeyDown=${event => keyDown(event, index)}
                    onClick=${() => select(tab)}>${tab[0].toUpperCase() + tab.slice(1)}</button>`)}
            </div>
            <p class="library-owner">${libraryOwnerCaption(selected, deviceId, device)}</p>
            <div id=${`${prefix}-panel-presets`} role="tabpanel" aria-labelledby=${`${prefix}-tab-presets`}
                hidden=${selected !== 'presets'} tabIndex="0">
                <${Presets} deviceId=${deviceId} status=${status} command=${command} commandBusy=${commandBusy}
                    onSelect=${onSelectPreset} showHeading=${false} presetMutations=${presetMutations} />
            </div>
            <div id=${`${prefix}-panel-favorites`} role="tabpanel" aria-labelledby=${`${prefix}-tab-favorites`}
                hidden=${selected !== 'favorites'} tabIndex="0">
                <${Favorites} deviceId=${deviceId} status=${status} favorites=${favorites}
                    commandBusy=${commandBusy} onPlay=${onPlayFavorite} />
            </div>
            <div id=${`${prefix}-panel-recents`} role="tabpanel" aria-labelledby=${`${prefix}-tab-recents`}
                hidden=${selected !== 'recents'} tabIndex="0">
                <${Recents} deviceId=${deviceId} physicalId=${device?.info?.device_id} presets=${status?.presets}
                    epoch=${status?.epoch} revision=${status?.recentsRevision}
                    refreshToken=${recentsRefreshToken} command=${command} commandBusy=${commandBusy}
                    onPlay=${onPlay} favorites=${favorites} showHeading=${false} presetMutations=${presetMutations} />
            </div>
        </section>
    `;
}
