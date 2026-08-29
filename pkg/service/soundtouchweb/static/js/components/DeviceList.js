import { h } from 'preact';
import { useState } from 'preact/hooks';
import htm from 'htm';
import {
    connectivityLabel,
    connectivityState,
    deviceAddress,
    sortDeviceEntries,
} from '../devicePresentation.mjs';
import { zoneCardPresentation } from '../zonePresentation.mjs';

const html = htm.bind(h);

const SORT_LS_KEY = 'aftertouch_device_sort';

function initialSortMode() {
    const stored = localStorage.getItem(SORT_LS_KEY);
    return stored === 'ip' || stored === 'name' ? stored : 'name';
}

function formatIPAddress(address) {
    const separator = address.lastIndexOf('.');
    if (separator === -1) {
        return html`<span class="device-ip-last">${address}</span>`;
    }

    return html`
        <span class="device-ip-prefix">${address.slice(0, separator + 1)}</span>
        <span class="device-ip-last">${address.slice(separator + 1)}</span>
    `;
}

function cardDetails(id, device, showIP, onRemove, nameID, zoneCard = null) {
    const { info, status } = device;
    const stereoPair = device.stereoPair;
    const np = status?.nowPlaying;
    const isPlaying = np?.PlayStatus === 'PLAY_STATE';
    const isStandby = !np || np.Source === 'STANDBY';
    const connectivity = connectivityState(device);
    const statusLabel = connectivityLabel(device);
    const address = deviceAddress(id, device);
    const indicatorClass = zoneCard?.health || connectivity;
    const indicatorLabel = zoneCard?.healthLabel || statusLabel;
    const showTechnicalDetails = info?.type || (!zoneCard && stereoPair) || (showIP && address);

    return html`
        <div class="device-header">
            <span class="device-name" id=${nameID}>${info?.name || id}</span>
            <span class="device-header-right">
                <span class="device-indicator ${indicatorClass}" role="status"
                      title=${indicatorLabel} aria-label=${indicatorLabel}></span>
                ${!stereoPair ? html`<button class="device-remove" title="Remove this device"
                        aria-label="Remove this device"
                        onClick=${(event) => { event.stopPropagation(); onRemove(id); }}>✕</button>` : null}
            </span>
        </div>
        ${!isStandby ? html`
            <div class="now-playing-mini">
                <span class="play-status">${isPlaying ? '▶' : '⏸'}</span>
                <span class="track-mini">${np.Track || np.StationName || np.Source}</span>
                ${np.Artist ? html`<span class="artist-mini"> — ${np.Artist}</span>` : null}
            </div>
        ` : null}
        ${isStandby ? html`<div class="standby-label">Standby</div>` : null}
        ${showTechnicalDetails ? html`
            <div class="device-type">
                ${info?.type || ''}
                ${showIP && address ? html`
                    <span class="device-ip" title=${address}>${formatIPAddress(address)}</span>
                ` : null}
                ${!zoneCard && stereoPair ? html`
                    <span class="stereo-pair-state ${stereoPair.degraded ? 'degraded' : ''}">
                        Stereo pair ${stereoPair.availableMemberCount}/${stereoPair.memberCount}
                    </span>
                ` : null}
            </div>
        ` : null}
    `;
}

function ZoneDeviceCard({ id, device, onSelect, onRemove, showIP }) {
    const zone = device.zone;
    const controlID = zone.masterControlId || id;
    const card = zoneCardPresentation(zone);
    const nameID = `zone-name-${controlID.replace(/[^a-zA-Z0-9_-]/g, '-')}`;

    function openFromKeyboard(event) {
        if (event.target !== event.currentTarget) return;
        if (event.key !== 'Enter' && event.key !== ' ') return;
        event.preventDefault();
        onSelect(controlID);
    }

    return html`
        <section class="device-card zone-card ${card.health}"
                 aria-labelledby=${nameID}>
            <div class="zone-card-open" role="button" tabindex="0"
                 onClick=${() => onSelect(controlID)} onKeyDown=${openFromKeyboard}>
                ${cardDetails(id, device, showIP, onRemove, nameID, card)}
                <div class="zone-card-summary">
                    <span class="zone-card-badge">${card.groupLabel}</span>
                    ${card.availabilityLabel ? html`
                        <span class="zone-card-availability" title=${card.availabilityTitle}>
                            ${card.availabilityLabel}
                        </span>
                    ` : null}
                </div>
            </div>
        </section>
    `;
}

function DeviceCard({ id, device, onSelect, onRemove, showIP }) {
    if (device.zone) {
        return html`<${ZoneDeviceCard} id=${id} device=${device} onSelect=${onSelect}
                    onRemove=${onRemove} showIP=${showIP} />`;
    }

    return html`
        <div class="device-card" onClick=${() => onSelect(id)}>
            ${cardDetails(id, device, showIP, onRemove)}
        </div>
    `;
}

export function DeviceList({ devices, isDiscovering, onSelect, onDiscover, onRemove }) {
    const [sortMode, setSortMode] = useState(initialSortMode);

    function changeSort(mode) {
        setSortMode(mode);
        localStorage.setItem(SORT_LS_KEY, mode);
    }

    const entries = sortDeviceEntries(Object.entries(devices), sortMode);

    return html`
        <div class="device-list-container">
        ${entries.length === 0
            ? html`
                <div class="empty-state" key="empty">
                    <div class="empty-icon ${isDiscovering ? 'radiating' : ''}">◉</div>
                    <p>${isDiscovering ? 'Searching for devices...' : 'No devices found on your network.'}</p>
                    <button class="btn-primary" onClick=${onDiscover} disabled=${isDiscovering}>
                        ${isDiscovering ? 'Discovering...' : 'Start Discovery'}
                    </button>
                </div>`
            : html`
                <div class="device-sort" key="sort">
                    <span class="device-sort-label">Sort by</span>
                    <button class="sort-btn ${sortMode === 'name' ? 'active' : ''}"
                            onClick=${() => changeSort('name')}>Name</button>
                    <button class="sort-btn ${sortMode === 'ip' ? 'active' : ''}"
                            onClick=${() => changeSort('ip')}>IP</button>
                </div>
                <div class="device-grid" key="grid">
                    ${entries.map(([id, device]) => html`
                        <${DeviceCard} key=${id} id=${id} device=${device} onSelect=${onSelect}
                            onRemove=${onRemove} showIP=${sortMode === 'ip'} />
                    `)}
                </div>
                <p class="device-list-note" key="note">
                    Removing a device clears it here. One that is still online may
                    reappear after the next discovery scan.
                </p>`
        }
        </div>
    `;
}
