import assert from 'node:assert/strict';
import test from 'node:test';

import {
    mergeZoneVolumeReadback,
    maxZoneVolume,
    previewZoneVolume,
    sameZoneMemberVolumes,
    zoneMemberVolumes,
} from '../static/js/zoneVolumePreview.mjs';

test('projects one group delta across logical members immediately', () => {
    assert.deepEqual(previewZoneVolume({ kitchen: 20, living: 35 }, 35, 42), {
        kitchen: 27,
        living: 42,
    });
});

test('keeps the pointer-down baseline when intermediate requests are coalesced', () => {
    const starting = { kitchen: 10, living: 20 };
    const muted = previewZoneVolume(starting, 20, 0);
    assert.deepEqual(muted, { kitchen: 0, living: 0 });
    assert.deepEqual(previewZoneVolume(starting, 20, 10), { kitchen: 0, living: 10 });
    assert.equal(maxZoneVolume(starting), 20);
});

test('extracts projected member volumes and merges authoritative readback', () => {
    const projected = zoneMemberVolumes({
        members: [
            { controlId: 'kitchen', actualVolume: 21 },
            { ip: '192.0.2.20', actualVolume: 34 },
            { controlId: 'offline', actualVolume: null },
        ],
    });
    assert.deepEqual(projected, { kitchen: 21, '192.0.2.20': 34 });

    assert.deepEqual(mergeZoneVolumeReadback(projected, {
        members: [
            { controlId: 'kitchen', actual: 25 },
            { controlId: '192.0.2.20', error: 'readback failed' },
        ],
    }), { kitchen: 25, '192.0.2.20': 34 });
});

test('compares complete member maps before dropping an optimistic overlay', () => {
    assert.equal(sameZoneMemberVolumes({ a: 10, b: 20 }, { b: 20, a: 10 }), true);
    assert.equal(sameZoneMemberVolumes({ a: 10 }, { a: 10, b: 20 }), false);
    assert.equal(sameZoneMemberVolumes({ a: 10 }, { a: 11 }), false);
});
