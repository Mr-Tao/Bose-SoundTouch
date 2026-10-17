import assert from 'node:assert/strict';
import test from 'node:test';
import { detailViewIdentity } from '../static/js/detailViewState.mjs';

test('view identity follows physical pair ownership, not connection or presentation updates', () => {
    const device = { info: { device_id: 'left', name: 'Living room' }, status: { epoch: 1 },
        stereoPair: { id: 'pair', masterDeviceId: 'left', members: [
            { deviceId: 'left', role: 'LEFT' }, { deviceId: 'right', role: 'RIGHT' },
        ] } };
    const key = detailViewIdentity(device);
    assert.equal(detailViewIdentity({ ...device, info: { ...device.info, name: 'Renamed' }, status: { epoch: 2 },
        stereoPair: { ...device.stereoPair, members: [...device.stereoPair.members].reverse() } }), key);
    for (const changed of [
        { ...device, info: { device_id: 'replacement' } },
        { ...device, stereoPair: { ...device.stereoPair, id: 'new-pair' } },
        { ...device, stereoPair: { ...device.stereoPair, masterDeviceId: 'right' } },
        { ...device, stereoPair: { ...device.stereoPair, members: [
            { deviceId: 'left', role: 'RIGHT' }, { deviceId: 'right', role: 'LEFT' },
        ] } },
    ]) assert.notEqual(detailViewIdentity(changed), key);
    assert.equal(detailViewIdentity({}), null);
});
