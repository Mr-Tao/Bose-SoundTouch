import assert from 'node:assert/strict';
import test from 'node:test';

import {
    createLatestWinsScheduler,
    shouldSurfaceLatestFinal,
} from '../static/js/latestWinsScheduler.mjs';

function deferred() {
    let resolve;
    let reject;
    const promise = new Promise((res, rej) => {
        resolve = res;
        reject = rej;
    });
    return { promise, resolve, reject };
}

function fakeClock() {
    let time = 0;
    let nextID = 1;
    const timers = new Map();

    function setTimer(callback, delay) {
        const id = nextID++;
        timers.set(id, { callback, at: time + delay });
        return id;
    }

    function advance(milliseconds) {
        const target = time + milliseconds;
        while (true) {
            const due = [...timers.entries()]
                .filter(([, timer]) => timer.at <= target)
                .sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
            if (!due) break;

            const [id, timer] = due;
            timers.delete(id);
            time = timer.at;
            timer.callback();
        }
        time = target;
    }

    return {
        now: () => time,
        setTimer,
        clearTimer: (id) => timers.delete(id),
        advance,
    };
}

async function flushPromises() {
    await Promise.resolve();
    await Promise.resolve();
}

test('coalesces pending levels and starts requests at least 200ms apart', async () => {
    const clock = fakeClock();
    const requests = [];
    const scheduler = createLatestWinsScheduler({
        send(value) {
            const request = deferred();
            requests.push({ value, startedAt: clock.now(), request });
            return request.promise;
        },
        now: clock.now,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
    });

    scheduler.queue(10);
    scheduler.queue(20);
    scheduler.queue(30);
    assert.deepEqual(requests.map(({ value }) => value), [10]);

    requests[0].request.resolve({ ok: true });
    await flushPromises();
    clock.advance(199);
    assert.equal(requests.length, 1);

    clock.advance(1);
    assert.deepEqual(requests.map(({ value, startedAt }) => [value, startedAt]), [
        [10, 0],
        [30, 200],
    ]);
});

test('never starts a second request while one is in flight', async () => {
    const clock = fakeClock();
    const requests = [];
    const scheduler = createLatestWinsScheduler({
        send(value) {
            const request = deferred();
            requests.push({ value, request });
            return request.promise;
        },
        now: clock.now,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
    });

    scheduler.queue(15);
    clock.advance(500);
    scheduler.queue(65);
    clock.advance(500);
    assert.deepEqual(requests.map(({ value }) => value), [15]);

    requests[0].request.resolve({ ok: true });
    await flushPromises();
    assert.deepEqual(requests.map(({ value }) => value), [15, 65]);
});

test('forced final input queues the same level again', async () => {
    const clock = fakeClock();
    const requests = [];
    const scheduler = createLatestWinsScheduler({
        send(value) {
            const request = deferred();
            requests.push({ value, request });
            return request.promise;
        },
        now: clock.now,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
    });

    scheduler.queue(40);
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    scheduler.queue(40);
    scheduler.queue(40, { force: true });
    clock.advance(200);

    assert.deepEqual(requests.map(({ value }) => value), [40, 40]);
});

test('marks only the response for the newest desired level as latest', async () => {
    const clock = fakeClock();
    const requests = [];
    const results = [];
    const scheduler = createLatestWinsScheduler({
        send(value) {
            const request = deferred();
            requests.push({ value, request });
            return request.promise;
        },
        now: clock.now,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
        onResult(_response, metadata) {
            results.push([metadata.value, metadata.isLatest]);
        },
    });

    scheduler.queue(25);
    scheduler.queue(75);
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    clock.advance(200);
    requests[1].request.resolve({ ok: true });
    await flushPromises();

    assert.deepEqual(results, [[25, false], [75, true]]);
});

test('surfaces failures only for the latest forced final request', () => {
    assert.equal(shouldSurfaceLatestFinal({ isLatest: true, final: true }), true);
    assert.equal(shouldSurfaceLatestFinal({ isLatest: true, final: false }), false);
    assert.equal(shouldSurfaceLatestFinal({ isLatest: false, final: true }), false);
    assert.equal(shouldSurfaceLatestFinal(undefined), false);
});
