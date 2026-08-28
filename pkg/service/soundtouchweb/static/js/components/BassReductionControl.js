import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { bassControlForStatus } from '../bassCapabilities.mjs';
import {
    confirmedSettingReadback,
    formatBassValue,
    newerSettingProjection,
    resetValue,
    settingFailureMessage,
    shouldClearSettingFailure,
    steppedValue,
    targetBassStatus,
} from '../soundSettingsPresentation.mjs';
import { SteppedSettingControl } from './SteppedSettingControl.js';

const html = htm.bind(h);

export function BassReductionControl({ target }) {
    const controlID = target?.controlId || '';
    const control = bassControlForStatus(targetBassStatus(target));
    const projectedValue = control.value;
    const projectedRevision = Number.isSafeInteger(target?.bassRevision) ? target.bassRevision : 0;
    const projectedValueRef = useRef(projectedValue);
    const projectionRevisionRef = useRef(projectedRevision);
    const projectionAvailableRef = useRef(control.available);
    const targetRef = useRef(controlID);
    const displayedTargetRef = useRef(controlID);
    const displayedRevisionRef = useRef(projectedRevision);
    const busyRef = useRef(false);
    const failedRevisionRef = useRef(null);
    const [localValue, setLocalValue] = useState(projectedValue);
    const [busy, setBusy] = useState(false);
    const [failure, setFailure] = useState('');

    projectedValueRef.current = projectedValue;
    projectionRevisionRef.current = projectedRevision;
    projectionAvailableRef.current = control.available;
    targetRef.current = controlID;

    useEffect(() => {
        if (displayedTargetRef.current !== controlID) {
            displayedTargetRef.current = controlID;
            displayedRevisionRef.current = projectedRevision;
            failedRevisionRef.current = null;
            setFailure('');
            setLocalValue(projectedValue);
            return;
        }
        if (busy || projectedRevision < displayedRevisionRef.current) return;
        displayedRevisionRef.current = projectedRevision;
        setLocalValue(projectedValue);
        if (shouldClearSettingFailure({
            failedRevision: failedRevisionRef.current,
            projectionRevision: projectedRevision,
            available: control.available,
            projectedValue,
            busy: false,
        })) {
            failedRevisionRef.current = null;
            setFailure('');
        }
    }, [controlID, projectedRevision, projectedValue, control.available, busy]);

    if (!control.available || !target?.controlId) return null;

    function adoptReplacementTarget() {
        displayedTargetRef.current = targetRef.current;
        displayedRevisionRef.current = projectionRevisionRef.current;
        failedRevisionRef.current = null;
        setFailure('');
        setLocalValue(projectedValueRef.current);
    }

    function adoptNewerProjection(afterRevision) {
        const projection = newerSettingProjection({
            projectionRevision: projectionRevisionRef.current,
            projectedValue: projectedValueRef.current,
            available: projectionAvailableRef.current,
            afterRevision,
        });
        if (projection === null) return false;

        displayedRevisionRef.current = projection.revision;
        setLocalValue(projection.actual);
        if (shouldClearSettingFailure({
            failedRevision: failedRevisionRef.current,
            projectionRevision: projection.revision,
            available: true,
            projectedValue: projection.actual,
            busy: false,
        })) {
            failedRevisionRef.current = null;
            setFailure('');
        }

        return true;
    }

    async function apply(level) {
        if (busyRef.current || !Number.isSafeInteger(level)) return;
        const requestTarget = target.controlId;
        const requestRevision = projectionRevisionRef.current;
        busyRef.current = true;
        setBusy(true);
        setFailure('');
        setLocalValue(level);

        try {
            const response = await api.bass(requestTarget, level);
            if (targetRef.current !== requestTarget) {
                adoptReplacementTarget();
                return;
            }
            const readback = confirmedSettingReadback(
                response, level, control.min, control.max, requestRevision);
            if (readback === null) {
                if (adoptNewerProjection(requestRevision)) return;
                failedRevisionRef.current = requestRevision;
                setLocalValue(projectedValueRef.current);
                setFailure(settingFailureMessage(response, 'Bass reduction'));
                return;
            }
            if (adoptNewerProjection(readback.revision)) return;

            displayedRevisionRef.current = readback.revision;
            setLocalValue(readback.actual);
            const message = settingFailureMessage(response, 'Bass reduction');
            failedRevisionRef.current = message ? readback.revision : null;
            setFailure(message);
        } catch (_error) {
            if (targetRef.current !== requestTarget) {
                adoptReplacementTarget();
                return;
            }
            if (adoptNewerProjection(requestRevision)) return;
            failedRevisionRef.current = requestRevision;
            setLocalValue(projectedValueRef.current);
            setFailure('Bass reduction update failed.');
        } finally {
            busyRef.current = false;
            setBusy(false);
        }
    }

    return html`<${SteppedSettingControl}
        label="Bass reduction"
        scopeLabel=${`Speaker · ${target.name || target.controlId}`}
        value=${localValue}
        min=${control.min}
        max=${control.max}
        defaultValue=${control.defaultValue}
        valueLabel=${formatBassValue(localValue)}
        defaultLabel=${formatBassValue(control.defaultValue)}
        disabled=${target.connectivity === 'offline'}
        busy=${busy}
        decreaseLabel="Reduce bass one step"
        increaseLabel="Restore bass one step"
        onDecrease=${() => apply(steppedValue(localValue, -1, control.min, control.max))}
        onIncrease=${() => apply(steppedValue(localValue, 1, control.min, control.max))}
        onReset=${() => apply(resetValue(localValue, control.defaultValue, control.min, control.max))}
        failure=${failure}
    />`;
}
