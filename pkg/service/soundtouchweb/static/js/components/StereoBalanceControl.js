import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import {
    balanceControlState,
    balanceFailureMessage,
    confirmedBalanceReadback,
} from '../stereoBalanceResult.mjs';
import {
    formatBalanceValue,
    newerSettingProjection,
    resetValue,
    steppedValue,
} from '../soundSettingsPresentation.mjs';
import { SteppedSettingControl } from './SteppedSettingControl.js';

const html = htm.bind(h);

function balanceDevice(device, member) {
    if (device) return device;

    return {
        stereoPair: member?.stereoPair || {},
        status: {
            balance: member?.balance,
            balanceRevision: member?.balanceRevision,
            connectivity: member?.connectivity,
            isConnected: member?.available,
        },
    };
}

export function shouldClearBalanceFailure({
    failedRevision,
    projectionRevision,
    enabled,
    projectedBalance,
    busy,
}) {
    return failedRevision !== null && Number.isSafeInteger(projectionRevision) &&
        projectionRevision > failedRevision &&
        enabled && Number.isSafeInteger(projectedBalance) &&
        !busy;
}

export function StereoBalanceControl({ id, device, member, scopeLabel = 'Stereo pair' }) {
    const control = balanceControlState(balanceDevice(device, member));
    const projectedBalance = control.value;
    const projectedRevision = control.revision;
    const projectedBalanceRef = useRef(projectedBalance);
    const projectionRevisionRef = useRef(projectedRevision);
    const projectionEnabledRef = useRef(control.enabled);
    const targetRef = useRef(id);
    const displayedTargetRef = useRef(id);
    const displayedRevisionRef = useRef(projectedRevision);
    const busyRef = useRef(false);
    const failedRevisionRef = useRef(null);
    const [localBalance, setLocalBalance] = useState(projectedBalance);
    const [busy, setBusy] = useState(false);
    const [failure, setFailure] = useState('');

    projectedBalanceRef.current = projectedBalance;
    projectionRevisionRef.current = projectedRevision;
    projectionEnabledRef.current = control.enabled;
    targetRef.current = id;

    useEffect(() => {
        if (displayedTargetRef.current !== id) {
            displayedTargetRef.current = id;
            displayedRevisionRef.current = projectedRevision;
            failedRevisionRef.current = null;
            setFailure('');
            setLocalBalance(projectedBalance);
            return;
        }
        if (busy || projectedRevision < displayedRevisionRef.current) return;
        displayedRevisionRef.current = projectedRevision;
        setLocalBalance(projectedBalance);
        if (shouldClearBalanceFailure({
            failedRevision: failedRevisionRef.current,
            projectionRevision: projectedRevision,
            enabled: control.enabled,
            projectedBalance,
            busy: false,
        })) {
            failedRevisionRef.current = null;
            setFailure('');
        }
    }, [id, projectedRevision, projectedBalance, control.enabled, busy]);

    if (!control.available) return null;

    function adoptReplacementTarget() {
        displayedTargetRef.current = targetRef.current;
        displayedRevisionRef.current = projectionRevisionRef.current;
        failedRevisionRef.current = null;
        setFailure('');
        setLocalBalance(projectedBalanceRef.current);
    }

    function adoptNewerProjection(afterRevision) {
        const projection = newerSettingProjection({
            projectionRevision: projectionRevisionRef.current,
            projectedValue: projectedBalanceRef.current,
            available: projectionEnabledRef.current,
            afterRevision,
        });
        if (projection === null) return false;

        displayedRevisionRef.current = projection.revision;
        setLocalBalance(projection.actual);
        if (shouldClearBalanceFailure({
            failedRevision: failedRevisionRef.current,
            projectionRevision: projection.revision,
            enabled: true,
            projectedBalance: projection.actual,
            busy: false,
        })) {
            failedRevisionRef.current = null;
            setFailure('');
        }

        return true;
    }

    async function apply(level) {
        if (busyRef.current || !control.enabled || !Number.isSafeInteger(level)) return;
        const requestTarget = id;
        const requestRevision = projectionRevisionRef.current;
        busyRef.current = true;
        setBusy(true);
        setFailure('');
        setLocalBalance(level);

        try {
            const response = await api.stereoBalance(requestTarget, level);
            if (targetRef.current !== requestTarget) {
                adoptReplacementTarget();
                return;
            }
            const readback = confirmedBalanceReadback(
                response, level, control.min, control.max, requestRevision);
            if (readback === null) {
                if (adoptNewerProjection(requestRevision)) return;
                failedRevisionRef.current = requestRevision;
                setLocalBalance(projectedBalanceRef.current);
                setFailure(balanceFailureMessage(response));
                return;
            }
            if (adoptNewerProjection(readback.revision)) return;

            displayedRevisionRef.current = readback.revision;
            setLocalBalance(readback.actual);
            const message = balanceFailureMessage(response);
            failedRevisionRef.current = message ? readback.revision : null;
            setFailure(message);
        } catch (_error) {
            if (targetRef.current !== requestTarget) {
                adoptReplacementTarget();
                return;
            }
            if (adoptNewerProjection(requestRevision)) return;
            failedRevisionRef.current = requestRevision;
            setLocalBalance(projectedBalanceRef.current);
            setFailure('Stereo balance update failed.');
        } finally {
            busyRef.current = false;
            setBusy(false);
        }
    }

    return html`<${SteppedSettingControl}
        label="Balance"
        scopeLabel=${scopeLabel}
        value=${localBalance}
        min=${control.min}
        max=${control.max}
        defaultValue=${control.defaultValue}
        valueLabel=${formatBalanceValue(localBalance)}
        defaultLabel=${formatBalanceValue(control.defaultValue)}
        disabled=${!control.enabled}
        busy=${busy}
        decreaseSymbol="←"
        increaseSymbol="→"
        decreaseLabel="Move balance one step left"
        increaseLabel="Move balance one step right"
        onDecrease=${() => apply(steppedValue(localBalance, -1, control.min, control.max))}
        onIncrease=${() => apply(steppedValue(localBalance, 1, control.min, control.max))}
        onReset=${() => apply(resetValue(localBalance, control.defaultValue, control.min, control.max))}
        failure=${failure}
    />`;
}
