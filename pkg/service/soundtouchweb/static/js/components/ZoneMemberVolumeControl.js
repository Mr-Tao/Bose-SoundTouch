import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { createLatestWinsScheduler, shouldSurfaceLatestFinal } from '../latestWinsScheduler.mjs';
import {
    clampVolume,
    maxReadbackActual,
    partialFailureMessage,
} from '../zoneVolumeResult.mjs';

const html = htm.bind(h);

export function memberVolumeControlState({ available, volume }) {
    if (!available) {
        return { disabled: true, readbackText: 'Volume unavailable.' };
    }
    if (!Number.isFinite(volume)) {
        return { disabled: true, readbackText: 'Volume readback unknown.' };
    }

    return { disabled: false, readbackText: '' };
}

export function ZoneMemberVolumeControl({
    zoneMasterId,
    memberId,
    ariaLabel,
    available,
    volume,
    previewVolume = null,
}) {
    const controlState = memberVolumeControlState({ available, volume });
    const hasKnownVolume = Number.isFinite(volume);
    const projectedVolume = hasKnownVolume ? clampVolume(volume) : null;
    const projectedVolumeRef = useRef(projectedVolume);
    const disabledRef = useRef(controlState.disabled);
    const interactionActiveRef = useRef(false);
    const interactionGenerationRef = useRef(0);
    const interactionDirtyRef = useRef(false);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localVolume, setLocalVolume] = useState(projectedVolume);
    const [isBusy, setIsBusy] = useState(false);
    const [failure, setFailure] = useState('');
    const inputID = `zone-member-volume-${zoneMasterId}-${memberId}`
        .replace(/[^a-zA-Z0-9_-]/g, '-');
    const readbackID = `${inputID}-readback`;

    projectedVolumeRef.current = projectedVolume;
    disabledRef.current = controlState.disabled;
    const displayedVolume = !controlState.disabled && Number.isFinite(previewVolume) &&
        !interactionActiveRef.current && !schedulerRef.current?.isActive()
        ? clampVolume(previewVolume)
        : localVolume;

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send: level => disabledRef.current
                ? { skippedDisabledMember: true }
                : api.zoneMemberVolume(zoneMasterId, memberId, level),
            onResult(response, metadata) {
                if (disabledRef.current || response?.skippedDisabledMember) return;
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
                if (disabledRef.current) return;
                if (shouldSurfaceLatestFinal(metadata)) {
                    setFailure('Member volume update failed.');
                }
            },
            onStateChange(next) {
                setIsBusy(next.active);
                if (!next.active && !interactionActiveRef.current &&
                    acceptedSequenceRef.current !== next.latestSequence) {
                    setLocalVolume(projectedVolumeRef.current);
                }
            },
        });
    }

    useEffect(() => () => schedulerRef.current.dispose(), []);
    useEffect(() => {
        if (!interactionActiveRef.current && !schedulerRef.current.isActive()) {
            setLocalVolume(projectedVolume);
        }
    }, [projectedVolume]);
    useEffect(() => {
        if (!controlState.disabled) return;

        interactionActiveRef.current = false;
        interactionDirtyRef.current = false;
        setFailure('');
        setLocalVolume(projectedVolume);
    }, [controlState.disabled, projectedVolume]);

    function beginVolumeInteraction() {
        if (disabledRef.current) return;
        if (interactionActiveRef.current) return;

        interactionGenerationRef.current += 1;
        interactionActiveRef.current = true;
        interactionDirtyRef.current = false;
    }

    function queueVolume(event, force) {
        if (disabledRef.current) return;

        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        if (!force) {
            beginVolumeInteraction();
            interactionDirtyRef.current = true;
        }
        setLocalVolume(level);
        setFailure('');
        schedulerRef.current.queue(level, {
            force,
            interactionGeneration: interactionGenerationRef.current,
        });
    }

    function finishVolume(event) {
        if (disabledRef.current) {
            interactionDirtyRef.current = false;
            interactionActiveRef.current = false;
            return;
        }
        if (!interactionDirtyRef.current) {
            interactionActiveRef.current = false;
            return;
        }
        interactionDirtyRef.current = false;
        interactionActiveRef.current = false;
        queueVolume(event, true);
    }

    return html`
        <div class="zone-member-volume-control" aria-busy=${isBusy ? 'true' : 'false'}
             role=${hasKnownVolume ? undefined : 'group'}
             aria-label=${hasKnownVolume ? undefined : ariaLabel}
             aria-describedby=${hasKnownVolume ? undefined : readbackID}>
            <div class="zone-member-volume-row">
                ${hasKnownVolume ? html`
                    <label class="zone-member-volume-label" for=${inputID}>Volume</label>
                    <input id=${inputID} type="range" class="zone-member-volume-slider"
                        min="0" max="100" value=${displayedVolume} aria-label=${ariaLabel}
                        disabled=${controlState.disabled}
                        aria-describedby=${controlState.readbackText ? readbackID : undefined}
                        onPointerDown=${() => {
                            beginVolumeInteraction();
                        }}
                        onInput=${event => queueVolume(event, false)}
                        onPointerUp=${finishVolume}
                        onPointerCancel=${finishVolume}
                        onChange=${finishVolume}
                        onBlur=${finishVolume} />
                ` : html`
                    <span class="zone-member-volume-label">Volume</span>
                    <div class="zone-member-volume-slider zone-member-volume-slider-unknown"
                         aria-hidden="true"></div>
                `}
                <output class="zone-member-volume-value" for=${hasKnownVolume ? inputID : undefined}>
                    ${hasKnownVolume ? displayedVolume : 'Unknown'}
                </output>
            </div>
            ${controlState.readbackText ? html`
                <div id=${readbackID} class="zone-member-volume-readback" role="status">
                    ${controlState.readbackText}
                </div>
            ` : null}
            ${failure ? html`<div class="zone-member-volume-failure" role="status">${failure}</div>` : null}
        </div>
    `;
}
