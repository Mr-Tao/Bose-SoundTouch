export function createLatestWinsScheduler({
    send,
    interval = 200,
    now = () => Date.now(),
    setTimer = (callback, delay) => setTimeout(callback, delay),
    clearTimer = (timer) => clearTimeout(timer),
    onResult = () => {},
    onError = () => {},
    onStateChange = () => {},
}) {
    let sequence = 0;
    let latestValue;
    let pending = null;
    let inFlight = null;
    let timer = null;
    let lastStartedAt = -Infinity;
    let disposed = false;

    function state() {
        return {
            active: pending !== null || inFlight !== null || timer !== null,
            inFlight: inFlight !== null,
            pending: pending !== null,
            latestSequence: sequence,
            latestValue,
        };
    }

    function emitState() {
        if (!disposed) onStateChange(state());
    }

    function settle(request, kind, value) {
        if (disposed || inFlight !== request) return;

        inFlight = null;
        const metadata = {
            ...request,
            isLatest: request.sequence === sequence && pending === null,
        };

        if (kind === 'result') onResult(value, metadata);
        else onError(value, metadata);

        pump();
        emitState();
    }

    function start() {
        const request = pending;
        pending = null;
        inFlight = request;
        lastStartedAt = now();
        emitState();

        let result;
        try {
            result = send(request.value, request);
        } catch (error) {
            settle(request, 'error', error);
            return;
        }

        Promise.resolve(result).then(
            (response) => settle(request, 'result', response),
            (error) => settle(request, 'error', error),
        );
    }

    function pump() {
        if (disposed || inFlight !== null || pending === null) return;

        const delay = Math.max(0, lastStartedAt + interval - now());
        if (delay === 0) {
            start();
            return;
        }

        if (timer === null) {
            timer = setTimer(() => {
                timer = null;
                pump();
                emitState();
            }, delay);
        }
    }

    function queue(value, { force = false } = {}) {
        if (disposed) return sequence;
        if (!force && value === latestValue) return sequence;

        sequence += 1;
        latestValue = value;
        pending = { sequence, value, final: force };
        pump();
        emitState();

        return sequence;
    }

    function dispose() {
        disposed = true;
        pending = null;
        if (timer !== null) clearTimer(timer);
        timer = null;
    }

    return {
        queue,
        dispose,
        isActive: () => state().active,
        getState: state,
    };
}
