import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { createLatestWinsScheduler } from '../zoneVolumeScheduler.mjs';
import { clampVolume, maxReadbackActual, partialFailureMessage } from '../zoneVolumeResult.mjs';

const html = htm.bind(h);

const SORT_LS_KEY = 'aftertouch_device_sort';

function initialSortMode() {
    const stored = localStorage.getItem(SORT_LS_KEY);
    return stored === 'ip' || stored === 'name' ? stored : 'name';
}

function sortEntries(entries, mode) {
    const copy = [...entries];
    if (mode === 'name') {
        // Sort by the speaker's display name, falling back to the map key (its IP)
        // when a device has no name yet.
        copy.sort(([idA, a], [idB, b]) => {
            const byName = (a?.info?.name || idA).localeCompare(
                b?.info?.name || idB, undefined, { sensitivity: 'base' });
            return byName || idA.localeCompare(idB, undefined, { numeric: true, sensitivity: 'base' });
        });
    } else {
        // IP mode uses the map key, ordered numerically so .2 precedes .10.
        copy.sort(([idA], [idB]) =>
            idA.localeCompare(idB, undefined, { numeric: true, sensitivity: 'base' }));
    }
    return copy;
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
    const connectivity = status?.connectivity || (status?.isConnected ? 'online' : 'offline');
    const connectivityLabel = connectivity.charAt(0).toUpperCase() + connectivity.slice(1);
    const showTechnicalDetails = info?.type || stereoPair || (showIP && info?.ip_address);

    return html`
        <div class="device-header">
            <span class="device-name" id=${nameID}>${info?.name || id}</span>
            <span class="device-indicator ${connectivity}" role="status" title=${connectivityLabel}
                  aria-label=${connectivityLabel}></span>
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
    const finalizedValueRef = useRef(null);
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
                    setFailure(response?.error || 'Group volume update failed.');
                    return;
                }

                const confirmed = maxReadbackActual(data);
                if (confirmed !== null) {
                    acceptedSequenceRef.current = metadata.sequence;
                    setLocalVolume(confirmed);
                }
                setFailure(partialFailureMessage(data));
            },
            onError(_error, metadata) {
                if (metadata.isLatest) setFailure('Group volume update failed.');
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
        if (!force) finalizedValueRef.current = null;
        setLocalVolume(level);
        setFailure('');
        schedulerRef.current.queue(level, { force });
    }

    function finishVolume(event) {
        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        draggingRef.current = false;
        if (finalizedValueRef.current === level) return;
        finalizedValueRef.current = level;
        queueVolume(event, true);
    }

    const unavailableCount = Math.max(0, zone.memberCount - zone.availableMemberCount);
    const availabilityLabel = zone.degraded
        ? `Degraded · ${zone.availableMemberCount}/${zone.memberCount} available`
        : `${zone.availableMemberCount}/${zone.memberCount} available`;

    return html`
        <section class="device-card zone-card ${zone.degraded ? 'degraded' : ''}"
                 aria-labelledby=${nameID} aria-busy=${isBusy ? 'true' : 'false'}>
            <button type="button" class="zone-card-open" onClick=${() => onSelect(controlID)}>
                ${cardDetails(id, device, showIP, nameID)}
                <div class="zone-card-summary">
                    <span class="zone-card-badge">Group · ${zone.memberCount}</span>
                    <span class="zone-card-availability ${zone.degraded ? 'degraded' : ''}"
                          title=${unavailableCount > 0 ? `${unavailableCount} unavailable` : availabilityLabel}>
                        ${availabilityLabel}
                    </span>
                </div>
            </button>
            <div class="zone-volume-row">
                <label class="zone-volume-label" for=${`${nameID}-volume`}>Group volume</label>
                <input id=${`${nameID}-volume`} type="range" class="zone-volume-slider"
                    min="0" max="100" value=${localVolume}
                    disabled=${zone.availableMemberCount === 0 || !hasProjectedVolume}
                    onPointerDown=${() => {
                        draggingRef.current = true;
                        finalizedValueRef.current = null;
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
            ${failure ? html`<div class="zone-volume-failure" role="status">${failure}</div>` : null}
        </section>
    `;
}

function DeviceCard({ id, device, onSelect, showIP }) {
    if (device.zone) {
        return html`<${ZoneDeviceCard} id=${id} device=${device} onSelect=${onSelect} showIP=${showIP} />`;
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

    const entries = sortEntries(Object.entries(devices), sortMode);

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
