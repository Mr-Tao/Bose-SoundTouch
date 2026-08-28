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

test('forced final input promotes the same in-flight level without another request', async () => {
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
            results.push([metadata.value, metadata.final, metadata.isLatest]);
        },
    });

    scheduler.queue(40, { interactionGeneration: 3 });
    scheduler.queue(40, { force: true, interactionGeneration: 3 });
    requests[0].request.resolve({ ok: true });
    await flushPromises();

    assert.deepEqual(requests.map(({ value }) => value), [40]);
    assert.deepEqual(results, [[40, true, true]]);
});

test('forced final input promotes the same pending level without another request', async () => {
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
            results.push([metadata.value, metadata.final, metadata.isLatest]);
        },
    });

    scheduler.queue(10, { interactionGeneration: 3 });
    scheduler.queue(40, { interactionGeneration: 3 });
    scheduler.queue(40, { force: true, interactionGeneration: 3 });
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    clock.advance(200);
    requests[1].request.resolve({ ok: true });
    await flushPromises();

    assert.deepEqual(requests.map(({ value }) => value), [10, 40]);
    assert.deepEqual(results, [
        [10, false, false],
        [40, true, true],
    ]);
});

test('forced final input reuses an already settled result without another request', async () => {
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
        onResult(response, metadata) {
            results.push([response.ok, metadata.value, metadata.final, metadata.isLatest]);
        },
    });

    scheduler.queue(40, { interactionGeneration: 3 });
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    scheduler.queue(40, { force: true, interactionGeneration: 3 });

    assert.deepEqual(requests.map(({ value }) => value), [40]);
    assert.deepEqual(results, [
        [true, 40, false, true],
        [true, 40, true, true],
    ]);
});

test('a new interaction retries the same value with a fresh request', async () => {
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

    scheduler.queue(40, { interactionGeneration: 3 });
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    scheduler.queue(40, { force: true, interactionGeneration: 3 });
    scheduler.queue(40, { interactionGeneration: 4 });
    clock.advance(200);

    assert.deepEqual(requests.map(({ value }) => value), [40, 40]);
});

test('a settled failure is surfaced at release but remains retryable next interaction', async () => {
    const clock = fakeClock();
    const requests = [];
    const errors = [];
    const scheduler = createLatestWinsScheduler({
        send(value) {
            const request = deferred();
            requests.push({ value, request });
            return request.promise;
        },
        now: clock.now,
        setTimer: clock.setTimer,
        clearTimer: clock.clearTimer,
        onError(error, metadata) {
            errors.push([error.message, metadata.interactionGeneration, metadata.final]);
        },
    });

    scheduler.queue(40, { interactionGeneration: 3 });
    requests[0].request.reject(new Error('offline'));
    await flushPromises();
    scheduler.queue(40, { force: true, interactionGeneration: 3 });
    scheduler.queue(40, { interactionGeneration: 4 });
    clock.advance(200);

    assert.deepEqual(requests.map(({ value }) => value), [40, 40]);
    assert.deepEqual(errors, [
        ['offline', 3, false],
        ['offline', 3, true],
    ]);
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
            results.push([metadata.value, metadata.isLatest, metadata.interactionGeneration]);
        },
    });

    scheduler.queue(25, { interactionGeneration: 4 });
    scheduler.queue(75, { interactionGeneration: 5 });
    requests[0].request.resolve({ ok: true });
    await flushPromises();
    clock.advance(200);
    requests[1].request.resolve({ ok: true });
    await flushPromises();

    assert.deepEqual(results, [[25, false, 4], [75, true, 5]]);
});

test('surfaces failures only for the latest forced final request', () => {
    assert.equal(shouldSurfaceLatestFinal({ isLatest: true, final: true }), true);
    assert.equal(shouldSurfaceLatestFinal({ isLatest: true, final: false }), false);
    assert.equal(shouldSurfaceLatestFinal({ isLatest: false, final: true }), false);
    assert.equal(shouldSurfaceLatestFinal(undefined), false);
});
