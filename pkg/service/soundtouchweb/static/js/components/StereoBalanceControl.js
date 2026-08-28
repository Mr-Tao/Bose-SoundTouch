import { h, htm, useEffect, useRef, useState } from '../dependencies.js';
import { api } from '../api.js';
import { createLatestWinsScheduler } from '../latestWinsScheduler.mjs';
import {
    balanceControlState,
    balanceFailureMessage,
    clampBalance,
    confirmedBalanceActual,
} from '../stereoBalanceResult.mjs';

const html = htm.bind(h);

function balanceDevice(device, member) {
    if (device) return device;

    return {
        stereoPair: member?.stereoPair || {},
        status: {
            balance: member?.balance,
            connectivity: member?.connectivity,
            isConnected: member?.available,
        },
    };
}

export function StereoBalanceControl({ id, device, member, ariaLabel = 'Balance' }) {
    const control = balanceControlState(balanceDevice(device, member));
    const projectedBalance = control.value;
    const projectedBalanceRef = useRef(projectedBalance);
    const controlRef = useRef(control);
    const draggingRef = useRef(false);
    const finalizedValueRef = useRef(null);
    const acceptedSequenceRef = useRef(0);
    const schedulerRef = useRef(null);
    const [localBalance, setLocalBalance] = useState(projectedBalance);
    const [isBusy, setIsBusy] = useState(false);
    const [failure, setFailure] = useState('');
    const inputID = `stereo-balance-${id.replace(/[^a-zA-Z0-9_-]/g, '-')}`;

    projectedBalanceRef.current = projectedBalance;
    controlRef.current = control;

    if (schedulerRef.current === null) {
        schedulerRef.current = createLatestWinsScheduler({
            send: level => api.stereoBalance(id, level),
            onResult(response, metadata) {
                if (!metadata.isLatest) return;

                const current = controlRef.current;
                const confirmed = confirmedBalanceActual(
                    response, metadata.value, current.min, current.max);
                if (confirmed === null) {
                    setFailure(balanceFailureMessage(response));
                    return;
                }

                acceptedSequenceRef.current = metadata.sequence;
                setLocalBalance(confirmed);
                setFailure(balanceFailureMessage(response));
            },
            onError(_error, metadata) {
                if (metadata.isLatest) setFailure('Stereo balance update failed.');
            },
            onStateChange(next) {
                setIsBusy(next.active);
                if (!next.active && !draggingRef.current &&
                    acceptedSequenceRef.current !== next.latestSequence) {
                    setLocalBalance(projectedBalanceRef.current);
                }
            },
        });
    }

    useEffect(() => () => schedulerRef.current.dispose(), []);
    useEffect(() => {
        if (!draggingRef.current && !schedulerRef.current.isActive()) {
            setLocalBalance(projectedBalance);
        }
    }, [projectedBalance, control.min, control.max, control.enabled]);

    function queueBalance(event, force) {
        const current = controlRef.current;
        const level = clampBalance(event.currentTarget.value, current.min, current.max);
        if (!current.enabled || level === null) return;
        if (!force) finalizedValueRef.current = null;
        setLocalBalance(level);
        setFailure('');
        schedulerRef.current.queue(level, { force });
    }

    function finishBalance(event) {
        const current = controlRef.current;
        const level = clampBalance(event.currentTarget.value, current.min, current.max);
        draggingRef.current = false;
        if (!current.enabled || level === null) return;
        if (finalizedValueRef.current === level) return;
        finalizedValueRef.current = level;
        queueBalance(event, true);
    }

    return html`
        <div class="stereo-balance-control ${control.enabled ? '' : 'unavailable'}"
             aria-busy=${isBusy ? 'true' : 'false'} aria-disabled=${control.enabled ? 'false' : 'true'}>
            <div class="stereo-balance-row">
                <label class="stereo-balance-label" for=${inputID}>Balance</label>
                <input id=${inputID} type="range" class="stereo-balance-slider"
                    min=${control.min} max=${control.max} value=${localBalance}
                    disabled=${!control.enabled} aria-label=${ariaLabel}
                    onPointerDown=${() => {
                        draggingRef.current = true;
                        finalizedValueRef.current = null;
                    }}
                    onInput=${event => queueBalance(event, false)}
                    onPointerUp=${finishBalance}
                    onPointerCancel=${finishBalance}
                    onChange=${finishBalance}
                    onBlur=${finishBalance} />
                <output class="stereo-balance-value" for=${inputID}>
                    ${Number.isFinite(localBalance) ? localBalance : '–'}
                </output>
            </div>
            ${failure ? html`<div class="stereo-balance-failure" role="status">${failure}</div>` : null}
        </div>
    `;
}
