import { h } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import htm from 'htm';
import { api } from '../api.js';
import { createLatestWinsScheduler, shouldSurfaceLatestFinal } from '../latestWinsScheduler.mjs';
import { clampVolume, maxReadbackActual, partialFailureMessage } from '../zoneVolumeResult.mjs';

const html = htm.bind(h);

export function ZoneMemberVolumeControl({
    zoneMasterId,
    memberId,
    ariaLabel,
    available,
    volume,
    previewVolume = null,
}) {
    const projectedVolume = Number.isFinite(volume) ? clampVolume(volume) : null;
    const disabled = !available || projectedVolume === null;
    const projectedVolumeRef = useRef(projectedVolume);
    const disabledRef = useRef(disabled);
    const interactionActiveRef = useRef(false);
    const interactionGenerationRef = useRef(0);
    const interactionDirtyRef = useRef(false);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localVolume, setLocalVolume] = useState(projectedVolume);
    const [isBusy, setIsBusy] = useState(false);
    const [failure, setFailure] = useState('');
    const inputId = `zone-member-volume-${zoneMasterId}-${memberId}`
        .replace(/[^a-zA-Z0-9_-]/g, '-');
    const readbackId = `${inputId}-readback`;

    projectedVolumeRef.current = projectedVolume;
    disabledRef.current = disabled;
    const displayedVolume = !disabled && Number.isFinite(previewVolume) &&
        !interactionActiveRef.current && !schedulerRef.current?.isActive()
        ? clampVolume(previewVolume)
        : localVolume;

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send: level => disabledRef.current
                ? { skippedDisabledMember: true }
                : api.zoneMemberVolume(zoneMasterId, memberId, level),
            onResult(response, metadata) {
                if (disabledRef.current || response?.skippedDisabledMember || !metadata.isLatest) return;

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
                if (!disabledRef.current && shouldSurfaceLatestFinal(metadata)) {
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
        if (!disabled) return;
        interactionActiveRef.current = false;
        interactionDirtyRef.current = false;
        setFailure('');
        setLocalVolume(projectedVolume);
    }, [disabled, projectedVolume]);

    function beginInteraction() {
        if (disabledRef.current || interactionActiveRef.current) return;
        interactionGenerationRef.current += 1;
        interactionActiveRef.current = true;
        interactionDirtyRef.current = false;
    }

    function queueVolume(event, force) {
        if (disabledRef.current) return;

        const level = clampVolume(parseInt(event.currentTarget.value, 10));
        if (!force) {
            beginInteraction();
            interactionDirtyRef.current = true;
        }
        setLocalVolume(level);
        setFailure('');
        schedulerRef.current.queue(level, {
            force,
            interactionGeneration: interactionGenerationRef.current,
        });
    }

    function finishInteraction(event) {
        if (disabledRef.current || !interactionDirtyRef.current) {
            interactionDirtyRef.current = false;
            interactionActiveRef.current = false;
            return;
        }
        interactionDirtyRef.current = false;
        interactionActiveRef.current = false;
        queueVolume(event, true);
    }

    return html`
        <div class="zone-member-volume-control" aria-busy=${isBusy ? 'true' : 'false'}
             role=${projectedVolume === null ? 'group' : undefined}
             aria-label=${projectedVolume === null ? ariaLabel : undefined}
             aria-describedby=${projectedVolume === null ? readbackId : undefined}>
            <div class="zone-member-volume-row">
                ${projectedVolume !== null ? html`
                    <label class="zone-member-volume-label" for=${inputId}>Volume</label>
                    <input id=${inputId} type="range" class="zone-member-volume-slider"
                        min="0" max="100" value=${displayedVolume} aria-label=${ariaLabel}
                        disabled=${disabled}
                        onPointerDown=${beginInteraction}
                        onInput=${event => queueVolume(event, false)}
                        onPointerUp=${finishInteraction}
                        onPointerCancel=${finishInteraction}
                        onChange=${finishInteraction}
                        onBlur=${finishInteraction} />
                ` : html`
                    <span class="zone-member-volume-label">Volume</span>
                    <div class="zone-member-volume-slider zone-member-volume-slider-unknown"
                         aria-hidden="true"></div>
                `}
                <output class="zone-member-volume-value" for=${projectedVolume !== null ? inputId : undefined}>
                    ${projectedVolume !== null ? displayedVolume : 'Unknown'}
                </output>
            </div>
            ${disabled ? html`
                <div id=${readbackId} class="zone-member-volume-readback" role="status">
                    ${available ? 'Volume readback unknown.' : 'Volume unavailable.'}
                </div>
            ` : null}
            ${failure ? html`<div class="zone-member-volume-failure" role="status">${failure}</div>` : null}
        </div>
    `;
}
