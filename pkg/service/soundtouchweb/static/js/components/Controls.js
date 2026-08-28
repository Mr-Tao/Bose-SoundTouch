import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { bassControlForStatus } from '../bassCapabilities.mjs';
import { createLatestWinsScheduler, shouldSurfaceLatestFinal } from '../latestWinsScheduler.mjs';
import { clampVolume, maxReadbackActual, partialFailureMessage } from '../zoneVolumeResult.mjs';

const html = htm.bind(h);

// Flat SVG icons using stroke/fill="currentColor" so they automatically
// follow the button's text colour in light mode, dark mode, and in the
// accent-inverted active state — no CSS filter needed.

function IconVolume({ muted = false, size = 20 }) {
    if (muted) {
        return html`<svg width=${size} height=${size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/>
            <line x1="23" y1="9" x2="17" y2="15"/>
            <line x1="17" y1="9" x2="23" y2="15"/>
        </svg>`;
    }
    return html`<svg width=${size} height=${size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/>
        <path d="M15.54 8.46a5 5 0 0 1 0 7.07"/>
    </svg>`;
}

function IconShuffle() {
    return html`<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <polyline points="16 3 21 3 21 8"/>
        <line x1="4" y1="20" x2="21" y2="3"/>
        <polyline points="21 16 21 21 16 21"/>
        <line x1="15" y1="15" x2="21" y2="21"/>
        <line x1="4" y1="4" x2="9" y2="9"/>
    </svg>`;
}

function IconRepeat({ one = false }) {
    return html`<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <polyline points="17 1 21 5 17 9"/>
        <path d="M3 11V9a4 4 0 0 1 4-4h14"/>
        <polyline points="7 23 3 19 7 15"/>
        <path d="M21 13v2a4 4 0 0 1-4 4H3"/>
        ${one && html`<text x="12" y="15" text-anchor="middle" font-size="8" font-weight="bold" stroke="none" fill="currentColor" font-family="sans-serif">1</text>`}
    </svg>`;
}

export function Controls({ deviceId, device }) {
    const status = device?.status;
    const zone = device?.zone;
    const controlsLogicalZone = Boolean(zone && !zone.isStandalone && zone.masterControlId === deviceId);
    const np = status?.nowPlaying;
    const isPlaying = np?.PlayStatus === 'PLAY_STATE';
    const projectedVolume = clampVolume(
        controlsLogicalZone && Number.isFinite(zone?.volume)
            ? zone.volume
            : (status?.volume?.ActualVolume ?? 0));
    const isMuted = status?.volume?.MuteEnabled ?? false;
    const shuffle = np?.ShuffleSetting ?? 'SHUFFLE_OFF';
    const repeat = np?.RepeatSetting ?? 'REPEAT_OFF';
    const bassControl = bassControlForStatus(status);
    const actualBass = bassControl.value;

    const projectedVolumeRef = useRef(projectedVolume);
    const controlTargetRef = useRef({ deviceId, group: controlsLogicalZone });
    const draggingRef = useRef(false);
    const interactionDirtyRef = useRef(false);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localVolume, setLocalVolume] = useState(projectedVolume);
    const [volumeBusy, setVolumeBusy] = useState(false);
    const [volumeFailure, setVolumeFailure] = useState('');
    const [localBass, setLocalBass] = useState(actualBass);

    projectedVolumeRef.current = projectedVolume;
    controlTargetRef.current = { deviceId, group: controlsLogicalZone };

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send(level, request) {
                const target = controlTargetRef.current;
                request.controlId = target.deviceId;
                request.group = target.group;
                return target.group
                    ? api.zoneVolume(target.deviceId, level)
                    : api.volume(target.deviceId, level);
            },
            onResult(response, metadata) {
                if (!metadata.isLatest) return;

                if (!response?.success) {
                    if (shouldSurfaceLatestFinal(metadata)) {
                        setVolumeFailure(response?.error || 'Volume update failed.');
                    }
                    return;
                }

                if (metadata.group) {
                    const data = response.data;
                    if (data?.requested !== metadata.value) {
                        if (shouldSurfaceLatestFinal(metadata)) {
                            setVolumeFailure('Group volume update failed.');
                        }
                        return;
                    }

                    const confirmed = maxReadbackActual(data);
                    if (confirmed !== null) {
                        acceptedSequenceRef.current = metadata.sequence;
                        setLocalVolume(confirmed);
                    }
                    if (shouldSurfaceLatestFinal(metadata)) {
                        setVolumeFailure(partialFailureMessage(data));
                    }
                    return;
                }

                acceptedSequenceRef.current = metadata.sequence;
                setLocalVolume(metadata.value);
                if (shouldSurfaceLatestFinal(metadata)) setVolumeFailure('');
            },
            onError(_error, metadata) {
                if (shouldSurfaceLatestFinal(metadata)) {
                    setVolumeFailure(metadata.group ? 'Group volume update failed.' : 'Volume update failed.');
                }
            },
            onStateChange(next) {
                setVolumeBusy(next.active);
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
    useEffect(() => { setLocalBass(actualBass); }, [actualBass]);

    const send = (key) => api.key(deviceId, key);

    function queueVolume(event, force) {
        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        if (!force) interactionDirtyRef.current = true;
        setLocalVolume(level);
        setVolumeFailure('');
        schedulerRef.current.queue(level, { force });
    }

    function finishVolume(event) {
        draggingRef.current = false;
        if (!interactionDirtyRef.current) return;
        interactionDirtyRef.current = false;
        queueVolume(event, true);
    }

    function onBassChange(e) {
        const val = parseInt(e.target.value, 10);
        setLocalBass(val);
        api.bass(deviceId, val);
    }

    function toggleShuffle() {
        send(shuffle === 'SHUFFLE_ON' ? 'SHUFFLE_OFF' : 'SHUFFLE_ON');
    }

    function cycleRepeat() {
        if (repeat === 'REPEAT_OFF') send('REPEAT_ALL');
        else if (repeat === 'REPEAT_ALL') send('REPEAT_ONE');
        else send('REPEAT_OFF');
    }

    return html`
        <div class="controls">
            <div class="transport">
                <button class="ctrl-btn" onClick=${() => send('PREV_TRACK')} title="Previous">⏮</button>
                <button class="ctrl-btn play-btn" onClick=${() => send(isPlaying ? 'PAUSE' : 'PLAY')}>
                    ${isPlaying ? '⏸' : '▶'}
                </button>
                <button class="ctrl-btn" onClick=${() => send('NEXT_TRACK')} title="Next">⏭</button>
                <button class="ctrl-btn ${isMuted ? 'active' : ''}" onClick=${() => send('MUTE')} title="Mute">
                    ${IconVolume({ muted: isMuted })}
                </button>
                <button class="ctrl-btn ${shuffle === 'SHUFFLE_ON' ? 'active' : ''}" onClick=${toggleShuffle} title="Shuffle">
                    ${IconShuffle()}
                </button>
                <button class="ctrl-btn ${repeat !== 'REPEAT_OFF' ? 'active' : ''}" onClick=${cycleRepeat} title=${repeat === 'REPEAT_ONE' ? 'Repeat one' : repeat === 'REPEAT_ALL' ? 'Repeat all' : 'Repeat'}>
                    ${IconRepeat({ one: repeat === 'REPEAT_ONE' })}
                </button>
            </div>
            <div class="volume-row" aria-busy=${volumeBusy ? 'true' : 'false'}>
                <span class="volume-icon">${IconVolume({ size: 16 })}</span>
                <input type="range" class="volume-slider" min="0" max="100"
                    value=${localVolume} aria-label=${controlsLogicalZone ? 'Group volume' : 'Volume'}
                    onPointerDown=${() => {
                        draggingRef.current = true;
                        interactionDirtyRef.current = false;
                    }}
                    onInput=${event => queueVolume(event, false)}
                    onPointerUp=${finishVolume}
                    onPointerCancel=${finishVolume}
                    onChange=${finishVolume}
                    onBlur=${finishVolume} />
                <span class="volume-value">${localVolume}</span>
            </div>
            ${volumeFailure ? html`<div class="volume-control-failure" role="status">${volumeFailure}</div>` : null}
            ${bassControl.available && html`
                <div class="bass-row">
                    <span class="bass-label">Bass</span>
                    <input type="range" class="volume-slider"
                        min=${bassControl.min} max=${bassControl.max} step="1"
                        value=${localBass} onInput=${onBassChange} />
                    <span class="volume-value">${localBass > 0 ? '+' : ''}${localBass}</span>
                </div>
            `}
        </div>
    `;
}
