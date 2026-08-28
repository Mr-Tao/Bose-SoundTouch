import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { connectivityLabel, connectivityState } from '../devicePresentation.mjs';
import { sortDeviceEntries } from '../deviceListPresentation.mjs';
import { createLatestWinsScheduler, shouldSurfaceLatestFinal } from '../latestWinsScheduler.mjs';
import {
    clampVolume,
    maxReadbackActual,
    partialFailureMessage,
} from '../zoneVolumeResult.mjs';
import { zoneCardPresentation } from '../zonePresentation.mjs';
import { StereoBalanceControl } from './StereoBalanceControl.js';

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

function cardDetails(id, device, showIP, nameID) {
    const { info, status } = device;
    const stereoPair = device.stereoPair;
    const np = status?.nowPlaying;
    const isPlaying = np?.PlayStatus === 'PLAY_STATE';
    const isStandby = !np || np.Source === 'STANDBY';
    const connectivity = connectivityState(device);
    const statusLabel = connectivityLabel(device);
    const showTechnicalDetails = info?.type || stereoPair || (showIP && info?.ip_address);

    return html`
        <div class="device-header">
            <span class="device-name" id=${nameID}>${info?.name || id}</span>
            <span class="device-indicator ${connectivity}" role="status" title=${statusLabel}
                  aria-label=${statusLabel}></span>
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
                ${showIP && info?.ip_address ? html`
                    <span class="device-ip" title=${info.ip_address}>${formatIPAddress(info.ip_address)}</span>
                ` : null}
                ${stereoPair ? html`
                    <span class="stereo-pair-state ${stereoPair.degraded ? 'degraded' : ''}">
                        Stereo pair ${stereoPair.availableMemberCount}/${stereoPair.memberCount}
                    </span>
                ` : null}
            </div>
        ` : null}
    `;
}

function ZoneDeviceCard({ id, device, onSelect, showIP }) {
    const zone = device.zone;
    const controlID = zone.masterControlId || id;
    const hasProjectedVolume = Number.isFinite(zone.volume);
    const projectedVolume = clampVolume(zone.volume);
    const projectedVolumeRef = useRef(projectedVolume);
    const draggingRef = useRef(false);
    const interactionDirtyRef = useRef(false);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localVolume, setLocalVolume] = useState(projectedVolume);
    const [isBusy, setIsBusy] = useState(false);
    const [failure, setFailure] = useState('');
    const nameID = `zone-name-${controlID.replace(/[^a-zA-Z0-9_-]/g, '-')}`;

    projectedVolumeRef.current = projectedVolume;

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send: level => api.zoneVolume(controlID, level),
            onResult(response, metadata) {
                if (!metadata.isLatest) return;

                const data = response?.data;
                if (!response?.success || data?.requested !== metadata.value) {
                    if (shouldSurfaceLatestFinal(metadata)) {
                        setFailure(response?.error || 'Group volume update failed.');
                    }
                    return;
                }

                const confirmed = maxReadbackActual(data);
                if (confirmed !== null) {
                    acceptedSequenceRef.current = metadata.sequence;
                    setLocalVolume(confirmed);
                }
                if (shouldSurfaceLatestFinal(metadata)) {
                    setFailure(partialFailureMessage(data));
                }
            },
            onError(_error, metadata) {
                if (shouldSurfaceLatestFinal(metadata)) {
                    setFailure('Group volume update failed.');
                }
            },
            onStateChange(next) {
                setIsBusy(next.active);
                if (!next.active && !draggingRef.current &&
                    acceptedSequenceRef.current !== next.latestSequence) {
                    setLocalVolume(projectedVolumeRef.current);
                }
            },
        });
    }

    useEffect(() => () => schedulerRef.current.dispose(), []);
    useEffect(() => {
        if (!draggingRef.current && !schedulerRef.current.isActive()) {
            setLocalVolume(projectedVolume);
        }
    }, [projectedVolume]);

    function queueVolume(event, force) {
        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        if (!force) interactionDirtyRef.current = true;
        setLocalVolume(level);
        setFailure('');
        schedulerRef.current.queue(level, { force });
    }

    function finishVolume(event) {
        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        draggingRef.current = false;
        if (!interactionDirtyRef.current) return;
        interactionDirtyRef.current = false;
        queueVolume(event, true);
    }

    const card = zoneCardPresentation(zone);

    return html`
        <section class="device-card zone-card ${zone.degraded ? 'degraded' : ''}"
                 aria-labelledby=${nameID} aria-busy=${isBusy ? 'true' : 'false'}>
            <button type="button" class="zone-card-open" onClick=${() => onSelect(controlID)}>
                ${cardDetails(id, device, showIP, nameID)}
                <div class="zone-card-summary">
                    <span class="zone-card-badge" title=${card.availabilityTitle}>${card.groupLabel}</span>
                    ${card.availabilityLabel ? html`
                        <span class="zone-card-availability degraded" title=${card.availabilityTitle}>
                            ${card.availabilityLabel}
                        </span>
                    ` : null}
                </div>
            </button>
            <div class="zone-volume-row">
                <label class="zone-volume-label" for=${`${nameID}-volume`}>Group volume</label>
                <input id=${`${nameID}-volume`} type="range" class="zone-volume-slider"
                    min="0" max="100" value=${localVolume}
                    disabled=${zone.availableMemberCount === 0 || !hasProjectedVolume}
                    onPointerDown=${() => {
                        draggingRef.current = true;
                        interactionDirtyRef.current = false;
                    }}
                    onInput=${event => queueVolume(event, false)}
                    onPointerUp=${finishVolume}
                    onPointerCancel=${finishVolume}
                    onChange=${finishVolume}
                    onBlur=${finishVolume} />
                <output class="zone-volume-value" for=${`${nameID}-volume`}>
                    ${hasProjectedVolume ? localVolume : '–'}
                </output>
            </div>
            ${device.stereoPair ? html`<${StereoBalanceControl} id=${controlID} device=${device} />` : null}
            ${failure ? html`<div class="zone-volume-failure" role="status">${failure}</div>` : null}
        </section>
    `;
}

function StereoPairDeviceCard({ id, device, onSelect, showIP }) {
    const nameID = `stereo-name-${id.replace(/[^a-zA-Z0-9_-]/g, '-')}`;

    return html`
        <section class="device-card stereo-balance-card" aria-labelledby=${nameID}>
            <button type="button" class="stereo-card-open" onClick=${() => onSelect(id)}>
                ${cardDetails(id, device, showIP, nameID)}
            </button>
            <${StereoBalanceControl} id=${id} device=${device} />
        </section>
    `;
}

function DeviceCard({ id, device, onSelect, showIP }) {
    if (device.zone) {
        return html`<${ZoneDeviceCard} id=${id} device=${device} onSelect=${onSelect} showIP=${showIP} />`;
    }
    if (device.stereoPair) {
        return html`<${StereoPairDeviceCard} id=${id} device=${device} onSelect=${onSelect} showIP=${showIP} />`;
    }

    return html`
        <button type="button" class="device-card" onClick=${() => onSelect(id)}>
            ${cardDetails(id, device, showIP)}
        </button>
    `;
}

export function DeviceList({ devices, isDiscovering, onSelect, onDiscover }) {
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
                            showIP=${sortMode === 'ip'} />
                    `)}
                </div>`
        }
        </div>
    `;
}
