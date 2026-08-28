import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { createLatestWinsScheduler, shouldSurfaceLatestFinal } from '../latestWinsScheduler.mjs';
import {
    clampVolume,
    maxReadbackActual,
    partialFailureMessage,
} from '../zoneVolumeResult.mjs';

const html = htm.bind(h);

export function ZoneMemberVolumeControl({
    zoneMasterId,
    memberId,
    ariaLabel,
    volume,
    previewVolume = null,
}) {
    const projectedVolume = clampVolume(volume);
    const projectedVolumeRef = useRef(projectedVolume);
    const draggingRef = useRef(false);
    const interactionDirtyRef = useRef(false);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localVolume, setLocalVolume] = useState(projectedVolume);
    const [isBusy, setIsBusy] = useState(false);
    const [failure, setFailure] = useState('');
    const inputID = `zone-member-volume-${zoneMasterId}-${memberId}`
        .replace(/[^a-zA-Z0-9_-]/g, '-');

    projectedVolumeRef.current = projectedVolume;
    const displayedVolume = Number.isFinite(previewVolume) &&
        !draggingRef.current && !schedulerRef.current?.isActive()
        ? clampVolume(previewVolume)
        : localVolume;

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send: level => api.zoneMemberVolume(zoneMasterId, memberId, level),
            onResult(response, metadata) {
                if (!metadata.isLatest) return;

                const data = response?.data;
                if (!response?.success || data?.requested !== metadata.value ||
                    data?.controlId !== memberId) {
                    if (shouldSurfaceLatestFinal(metadata)) {
                        setFailure(response?.error || 'Member volume update failed.');
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
                    setFailure('Member volume update failed.');
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

    return html`
        <div class="zone-member-volume-control" aria-busy=${isBusy ? 'true' : 'false'}>
            <div class="zone-member-volume-row">
                <label class="zone-member-volume-label" for=${inputID}>Volume</label>
                <input id=${inputID} type="range" class="zone-member-volume-slider"
                    min="0" max="100" value=${displayedVolume} aria-label=${ariaLabel}
                    onPointerDown=${() => {
                        draggingRef.current = true;
                        interactionDirtyRef.current = false;
                    }}
                    onInput=${event => queueVolume(event, false)}
                    onPointerUp=${finishVolume}
                    onPointerCancel=${finishVolume}
                    onChange=${finishVolume}
                    onBlur=${finishVolume} />
                <output class="zone-member-volume-value" for=${inputID}>
                    ${displayedVolume}
                </output>
            </div>
            ${failure ? html`<div class="zone-member-volume-failure" role="status">${failure}</div>` : null}
        </div>
    `;
}
